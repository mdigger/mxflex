package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/mdigger/log"
	"github.com/mdigger/rest"
	"golang.org/x/crypto/acme/autocert"
)

// информация о сервисе и версия
var (
	appName = "MXFlex" // название сервиса
	version = "1.9"    // версия
	date    = ""       // дата сборки
	git     = ""       // версия git

	agent      = appName + "/" + version            // имя агента и сервера
	configName = strings.ToLower(appName) + ".toml" // имя файла с хранилищем токенов
)

func init() {
	// инициализируем разбор параметров запуска сервиса
	flag.StringVar(&configName, "config", configName, "configuration `filename`")
	var logLevel = int(log.INFO)
	flag.IntVar(&logLevel, "log", logLevel, "log `level`")
	flag.Parse()

	// настраиваем вывод лога
	log.SetLevel(log.Level(logLevel))
	if strings.Contains(os.Getenv("LOG"), "DEBUG") && log.IsTTY() {
		log.SetFormat(log.Color)
	}
	// выводим информацию о текущей версии
	var verInfoFields = []interface{}{
		"name", appName,
		"version", version,
	}
	if date != "" {
		verInfoFields = append(verInfoFields, "builded", date)
	}
	if git != "" {
		verInfoFields = append(verInfoFields, "git", git)
		agent += " (" + git + ")"
	}
	log.Info("service info", verInfoFields...)
}

func main() {
	// выводим в лог ключ для подписи токенов
	log.Debug("jwt sign key", "key",
		base64.RawURLEncoding.EncodeToString(jwtConfig.Key.([]byte)))
	// загружаем и разбираем конфигурационный файл
	config, err := LoadConfig(configName)
	if err != nil {
		log.IfErr(err, "config error")
		os.Exit(1)
	}
	// подключаемся к серверу MX
	log.Info("connecting to mx", "host", config.MX.Addr, "login", config.MX.Login)
	handler, err := NewHTTPHandler(
		config.MX.Addr, config.MX.Login, config.MX.Password)
	if err != nil {
		log.IfErr(err, "mx connection error")
		os.Exit(2)
	}
	defer handler.Close()

	// инициализируем обработку HTTP запросов
	var mux = &rest.ServeMux{
		Headers: map[string]string{
			"Server": agent,
		},
		Logger: log.New("HTTP"),
	}
	var htmlFile = filepath.Join("html", "index.html")
	mux.Handle("GET", "/", rest.File(htmlFile))
	mux.Handle("GET", "/"+filepath.Base(htmlFile), rest.Redirect("/"))
	mux.Handle("GET", "/*file", rest.Files(filepath.Dir(htmlFile)))

	mux.Handle("POST", "/api/login", handler.Login)
	mux.Handle("GET", "/api/logout", handler.Logout)
	mux.Handle("GET", "/api/contacts", handler.Contacts)
	mux.Handle("POST", "/api/call", handler.MakeCall)
	// mux.Handle("POST", "/api/call/hold", handler.CallHold)
	mux.Handle("POST", "/api/call/hangup", handler.CallHangup)
	mux.Handle("POST", "/api/call/transfer", handler.CallTransfer)
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
		ErrorLog:     log.StdLogger(log.WARN, "HTTP"),
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
					return log.Error("unsupported https host", "host", host)
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
		log.Info("starting http server",
			"address", server.Addr,
			"tls", canCert,
			"host", httphost)
		var err error
		if canCert {
			err = server.ListenAndServeTLS("", "")
		} else {
			err = server.ListenAndServe()
		}
		if err != nil {
			log.IfErr(err, "http server stopped")
			os.Exit(2)
		}
	}()
}
