package main

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/mdigger/log"
	"github.com/mdigger/rest"
	"golang.org/x/crypto/acme/autocert"
)

// Proxy описывает основной HTTP сервер для работы с MX.
type Proxy struct {
	handler *HTTPHandler // обработчик API
	server  *http.Server
}

// NewProxy запускает новый сервер.
func NewProxy(config *Config) (*Proxy, error) {
	config.mu.RLock()
	defer config.mu.RUnlock()
	if config.MX.Host == "" || config.MX.Login == "" || len(config.MX.Password) == 0 {
		return nil, errors.New("mx not configured")
	}
	handler, err := NewHTTPHandler(
		config.MX.Host, config.MX.Login, string(config.MX.Password))
	if err != nil {
		return nil, err
	}
	slog := log.New("http")
	// инициализируем обработку HTTP запросов
	var mux = &rest.ServeMux{
		Headers: map[string]string{
			"Server": agent,
		},
		Logger: slog,
	}
	var htmlFile = filepath.Join("html", "index.html")
	mux.Handle("GET", "/", rest.File(htmlFile))
	mux.Handle("GET", "/"+filepath.Base(htmlFile), rest.Redirect("/"))
	mux.Handle("GET", "/*file", rest.Files(filepath.Dir(htmlFile)))
	// обработчики API
	mux.Handle("POST", "/api/login", handler.Login)
	mux.Handle("GET", "/api/logout", handler.Logout)
	mux.Handle("GET", "/api/contacts", handler.Contacts)
	mux.Handle("POST", "/api/call", handler.MakeCall)
	mux.Handle("POST", "/api/call/hangup", handler.CallHangup)
	mux.Handle("POST", "/api/call/transfer", handler.CallTransfer)
	mux.Handle("GET", "/api/events", handler.Events)
	mux.Handle("GET", "/api/info", handler.ConnectionInfo)
	// дополнительные данные
	mux.Handle("GET", "/rules", func(c *rest.Context) error {
		config.mu.RLock()
		defer config.mu.RUnlock()
		return c.Write(rest.JSON{"params": config.Params})
	})

	// инициализируем HTTP сервер
	var server = &http.Server{
		Handler:     mux,
		ReadTimeout: time.Second * 10,
		ErrorLog:    slog.StdLog(log.WARN),
	}
	if host := config.ServerURL(); strings.HasPrefix(host, "https://") {
		server.Addr = ":https"
		host := host[8:]
		if indx := strings.IndexAny(host, ":/"); indx > 0 {
			host = host[:indx]
		}
		manager := autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(host),
			Email:      "dmitrys@xyzrd.com",
			Cache:      autocert.DirCache("letsEncrypt.cache"),
		}
		server.TLSConfig = &tls.Config{
			GetCertificate: manager.GetCertificate,
		}
		// поддержка получения сертификата Let's Encrypt
		go http.ListenAndServe(":http", manager.HTTPHandler(nil))
	} else {
		server.Addr = ":http"
		if hostURL, err := url.Parse(host); err == nil {
			if port := hostURL.Port(); port != "" {
				server.Addr = ":" + port
			}
		}
	}
	go func() {
		slog.Info("service started", "addr", server.Addr)
		var err error
		if server.Addr == ":https" {
			err = server.ListenAndServeTLS("", "")
		} else {
			err = server.ListenAndServe()
		}
		slog.Info("service stopped", err)
		config.mu.Lock()
		config.err = err
		config.mu.Unlock()
	}()
	return &Proxy{handler: handler, server: server}, nil
}

// Close закрывает соединение с сервером MX и останавливает сервер.
func (p *Proxy) Close() error {
	p.server.Close()
	return p.handler.Close()
}
