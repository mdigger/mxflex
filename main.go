package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/mdigger/log"
	"github.com/mdigger/mx"
	"github.com/mdigger/rest"
	"golang.org/x/crypto/acme/autocert"
)

// информация о сервисе и версия
var (
	appName = "MXFlex" // название сервиса
	version = "1.8"    // версия
	date    = ""       // дата сборки
	git     = ""       // версия git

	agent      = appName + "/" + version            // имя агента и сервера
	configName = strings.ToLower(appName) + ".yaml" // имя файла с хранилищем токенов
	debug      = false                              // флаг вывода отладочной информации
	cstaOutput = false                              // флаг вывода команд и ответов CSTA
)

func init() {
	// инициализируем разбор параметров запуска сервиса
	flag.StringVar(&configName, "config", configName, "configuration `filename`")
	flag.BoolVar(&debug, "debug", debug, "debug output")
	var logFlags = log.Lindent | log.LstdFlags
	flag.IntVar(&logFlags, "logflag", logFlags, "log flags")
	flag.BoolVar(&cstaOutput, "csta", cstaOutput, "CSTA output")
	flag.Parse()

	// подменяем символы на сообщения
	log.Strings = map[log.Level]string{
		log.DebugLevel:   "DEBUG",
		log.InfoLevel:    "INFO",
		log.WarningLevel: "WARN",
		log.ErrorLevel:   "︎ERROR",
	}
	log.SetFlags(logFlags) // устанавливаем флаги вывода в лог
	// разрешаем вывод отладочной информации, включая вывод команд CSTA
	if debug {
		mx.LogINOUT = map[bool]string{true: "EVN", false: "CMD"}
		log.SetLevel(log.DebugLevel)
	}
	// выводим информацию о текущей версии
	var verInfoFields = log.Fields{
		"name":    appName,
		"version": version,
	}
	if date != "" {
		verInfoFields["builded"] = date
	}
	if git != "" {
		verInfoFields["git"] = git
		agent += " (" + git + ")"
	}
	log.WithFields(verInfoFields).Info("service info")
}

func main() {
	// загружаем и разбираем конфигурационный файл
	config, err := LoadConfig(configName)
	if err != nil {
		log.WithError(err).Error("config error")
		os.Exit(1)
	}
	// подключаемся к серверу MX
	log.WithFields(log.Fields{
		"host":  config.MX.Addr,
		"login": config.MX.Login,
	}).Info("connecting to mx")
	monitor, err := NewMXMonitor(config.MX.Addr, config.MX.Login, config.MX.Password)
	if err != nil {
		log.WithError(err).Error("mx connection error")
		os.Exit(2)
	}
	defer monitor.Close()
	go monitor.monitoring() // запускаем мониторинг звонков

	// инициализируем обработку HTTP запросов
	var mux = &rest.ServeMux{
		Headers: map[string]string{
			"Server": agent,
		},
		Logger: log.WithField("type", "http"),
	}
	var htmlFile = filepath.Join("html", "index.html")
	mux.Handle("GET", "/", rest.File(htmlFile))
	mux.Handle("GET", "/"+filepath.Base(htmlFile), rest.Redirect("/"))
	mux.Handle("GET", "/*file", rest.Files(filepath.Dir(htmlFile)))

	var handler = &Handler{monitor: monitor}
	mux.Handle("POST", "/api/login", handler.Login)
	mux.Handle("GET", "/api/logout", handler.Logout)
	mux.Handle("GET", "/api/contacts", handler.Contacts)
	mux.Handle("POST", "/api/call", handler.MakeCall)
	mux.Handle("GET", "/api/events", handler.Events)
	mux.Handle("GET", "/api/info", handler.ConnectionInfo)

	startHTTPServer(mux, config.Host)     // запускаем HTTP сервер
	monitorSignals(os.Interrupt, os.Kill) // ожидаем сигнала остановки
}

// monitorSignals запускает мониторинг сигналов и возвращает значение, когда
// получает сигнал. В качестве параметров передается список сигналов, которые
// нужно отслеживать.
func monitorSignals(signals ...os.Signal) os.Signal {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, signals...)
	return <-signalChan
}

// StartHTTPServer запускает HTTP сервер.
func startHTTPServer(mux http.Handler, host string) {
	// инициализируем HTTP сервер
	var server = &http.Server{
		Handler:      mux,
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Minute * 5,
	}
	// анализируем порт
	var httphost, port, err = net.SplitHostPort(host)
	if err, ok := err.(*net.AddrError); ok && err.Err == "missing port in address" {
		httphost = err.Addr
	}
	var canCert = httphost != "localhost" &&
		!strings.HasSuffix(httphost, ".local") &&
		net.ParseIP(httphost) == nil && httphost != "" &&
		(port == "443" || port == "https" || port == "")
	// добавляем автоматическую поддержку TLS сертификатов для сервиса
	if canCert {
		manager := autocert.Manager{
			Prompt: autocert.AcceptTOS,
			HostPolicy: func(_ context.Context, host string) error {
				if host != httphost {
					log.WithField("host", host).Error("unsupported https host")
					return errors.New("acme/autocert: host not configured")
				}
				return nil
			},
			Email: "dmitrys@xyzrd.com",
			Cache: autocert.DirCache("letsEncrypt.cache"),
		}
		server.TLSConfig = &tls.Config{
			GetCertificate: manager.GetCertificate,
		}
		server.Addr = ":https"
	} else if port == "" {
		server.Addr = net.JoinHostPort(httphost, "http")
	} else {
		server.Addr = net.JoinHostPort(httphost, port)
	}
	// запускаем HTTP сервер
	go func() {
		log.WithFields(log.Fields{
			"address": server.Addr,
			"tls":     canCert,
			"host":    httphost,
		}).Info("starting http server")
		var err error
		if canCert {
			err = server.ListenAndServeTLS("", "")
		} else {
			err = server.ListenAndServe()
		}
		if err != nil {
			log.WithError(err).Error("http server stopped")
			os.Exit(2)
		}
	}()
}
