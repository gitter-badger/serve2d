package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path"

	"github.com/joushou/serve2"
	"github.com/joushou/serve2/proto"
)

var (
	server    *serve2.Server
	conf      Config
	confReady bool
	logger    func(string, ...interface{})
)

// Config is the top-level config
type Config struct {
	Address   string
	LogStdout bool   `json:"logStdout,omitempty"`
	LogFile   string `json:"logFile,omitempty"`
	MaxRead   int    `json:"maxRead,omitempty"`
	Protocols []Protocol
}

// Protocol is the part of config defining individual protocols
type Protocol struct {
	Kind      string
	AsDefault bool                   `json:"default,omitempty"`
	Conf      map[string]interface{} `json:"conf,omitempty"`
}

func logit(format string, msg ...interface{}) {
	defer func() {
		if r := recover(); r != nil {
			println("Log failed: ", r)
			panic(r)
		}
	}()

	if logger != nil || !confReady {
		log.Printf(format, msg...)
	}
}

type httpHandler struct {
	path, defaultFile, notFoundMsg string
}

func (h httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		return
	}

	// We add the "./" to make things relative
	p := "." + path.Clean(r.URL.Path)

	if p == "./" {
		p += h.defaultFile
	}
	// We then put the origin on there
	p = path.Join(h.path, p)

	content, err := ioutil.ReadFile(p)
	if err != nil {
		logit("http could not read file %s: %v", p, err)
		w.WriteHeader(404)
		fmt.Fprintf(w, "%s", h.notFoundMsg)
		return
	}

	fmt.Fprintf(w, "%s", content)
}

func main() {
	defer func() {
		if err := recover(); err != nil {
			logit("Panicked: %s", err)
		}
	}()

	if len(os.Args) <= 1 {
		panic("Missing configuration path")
	}

	path := os.Args[1]

	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		logit("Reading configuration failed")
		panic(err)
	}

	err = json.Unmarshal(bytes, &conf)
	if err != nil {
		logit("Parsing configuration failed")
		panic(err)
	}

	confReady = true

	server = serve2.New()

	if conf.LogStdout && conf.LogFile != "" {
		panic("Unable to both log to stdout and to logfile")
	}

	if conf.LogStdout || conf.LogFile != "" {
		if conf.LogFile != "" {
			file, err := os.Create(conf.LogFile)
			if err != nil {
				logit("Failed to open logfile: %s", conf.LogFile)
				panic(err)
			}
			log.SetOutput(file)
		}

		logger = log.Printf
		server.Logger = log.Printf
	}

	if conf.MaxRead != 0 {
		server.BytesToCheck = conf.MaxRead
	}

	logit("Maximum buffer size: %d", server.BytesToCheck)

	l, err := net.Listen("tcp", conf.Address)
	if err != nil {
		logit("Listen on [%s] failed", conf.Address)
		panic(err)
	}

	logit("Listening on: %s", conf.Address)

	for _, v := range conf.Protocols {
		var (
			handler serve2.ProtocolHandler
			err     error
		)
		switch v.Kind {
		case "proxy":
			magic, mok := v.Conf["magic"].(string)
			magicSlice, sok := v.Conf["magic"].([]interface{})
			if !mok && !sok {
				panic("Proxy declaration is missing valid magic")
			}

			target, ok := v.Conf["target"].(string)
			if !ok {
				panic("Proxy declaration is missing valid target")
			}

			if mok {
				handler = proto.NewProxy([]byte(magic), "tcp", target)
			} else {
				magics := make([][]byte, len(magicSlice))
				for i := range magicSlice {
					magic, ok := magicSlice[i].(string)
					if !ok {
						panic("magic declaration invalid")
					}
					magics[i] = []byte(magic)
				}
				handler = proto.NewMultiProxy(magics, "tcp", target)
			}
		case "tls":
			cert, ok := v.Conf["cert"].(string)
			if !ok {
				panic("TLS declaration is missing valid certificate")
			}

			key, ok := v.Conf["key"].(string)
			if !ok {
				panic("TLS declaration is missing valid key")
			}

			var protos []string
			y, ok := v.Conf["protos"].([]interface{})
			if !ok {
				panic("TLS protos declaration invalid")
			}

			for _, x := range y {
				proto, ok := x.(string)
				if !ok {
					panic("TLS protos declaration invalid")
				}
				protos = append(protos, proto)
			}

			handler, err = proto.NewTLS(protos, cert, key)
			if err != nil {
				logit("TLS configuration failed")
				panic(err)
			}
		case "http":
			h := httpHandler{}
			msg, msgOk := v.Conf["notFoundMsg"]
			filename, fileOk := v.Conf["notFoundFile"]
			if fileOk && msgOk {
				panic("HTTP notFoundMsg and notFoundFile declared simultaneously")
			}

			if !msgOk && !fileOk {
				h.notFoundMsg = "<!DOCTYPE html><html><body><h1>404</h1></body></html>"
			} else if msgOk {
				h.notFoundMsg, msgOk = msg.(string)
				if !msgOk {
					panic("HTTP notFoundMsg declaration invalid")
				}
			} else if fileOk {
				f, ok := filename.(string)
				if !ok {
					panic("HTTP notFoundFile declaration invalid")
				}

				x, err := ioutil.ReadFile(f)
				if err != nil {
					logit("HTTP unable to open notFoundFile")
					panic(err)
				}
				h.notFoundMsg = string(x)
			}

			c, ok := v.Conf["defaultFile"]
			if !ok {
				h.defaultFile = "index.html"
			} else {
				h.defaultFile, ok = c.(string)
				if !ok {
					panic("HTTP defaultFile declaration invalid")
				}
			}

			h.path, ok = v.Conf["path"].(string)
			if !ok {
				panic("HTTP path declaration invalid")
			}

			handler = proto.NewHTTP(h)
		case "echo":
			handler = proto.NewEcho()
		case "discard":
			handler = proto.NewDiscard()
		default:
			panic("Unknown kind: " + v.Kind)
		}

		if v.AsDefault {
			server.DefaultProtocol = handler
		} else {
			server.AddHandler(handler)
		}
	}

	if server.DefaultProtocol != nil {
		logit("Default protocol set to: %v", server.DefaultProtocol)
	}

	server.Serve(l)
}
