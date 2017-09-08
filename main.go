package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mdigger/csta"
	"github.com/mdigger/log"
	"github.com/mdigger/rest"
	"github.com/mdigger/sse"
	"golang.org/x/crypto/acme/autocert"
)

var (
	appName = "mxflex"     // название сервиса
	version = "0.6"        // версия
	date    = "2017-09-08" // дата сборки
	git     = ""           // версия git

	phone string
)

func main() {
	var exts extList
	// exts.Set("3095,3099,3044")
	flag.Var(&exts, "ext", "comma-separated list of monitored `extensions`")
	var mxaddr = new(csta.Addr)
	// mxaddr, _ = csta.ParseURL("mx://d3test:981211@89.185.246.134")
	flag.Var(mxaddr, "mx", "mx url string in format `mx://login:password/host`")
	var host = "localhost:8080"
	flag.StringVar(&host, "host", host, "http server `host` name")
	flag.StringVar(&phone, "phone", phone, "outgoing phone `number`")
	var cstaOutput bool
	flag.BoolVar(&cstaOutput, "csta", cstaOutput, "CSTA output")
	var color bool
	flag.BoolVar(&color, "color", color, "color CSTA output")
	var logFlags = log.LstdFlags | log.Lindent
	flag.IntVar(&logFlags, "logflag", logFlags, "log flags")
	flag.Parse()

	log.SetFlags(logFlags)
	log.WithFields(log.Fields{
		"name":     appName,
		"version":  version,
		"build":    date,
		"revision": build,
		"git":      git,
	}).Info("starting service")
	log.SetLevel(log.DebugLevel)
	if cstaOutput {
		csta.SetLogOutput(os.Stdout)
		csta.SetLogFlags(0)
		if color {
			csta.LogTTY = true
		}
	}

	// инициализируем брокеров
	if exts.Len() == 0 {
		log.Error("no monitoring exts")
		os.Exit(2)
	}
	if phone == "" {
		log.Error("no outgoing phone number")
		os.Exit(2)
	}
	if mxaddr == nil {
		log.Error("no mx address")
		os.Exit(2)
	}
	var brokers = make(map[string]*sse.Broker, exts.Len())
	for _, ext := range exts.List() {
		brokers[ext] = sse.New()
	}
	// инициализируем сервис
	var service = &Service{
		mxaddr:  mxaddr.Host,
		brokers: brokers,
	}
	// инициализируем обработку HTTP запросов
	var mux = &rest.ServeMux{
		Headers: map[string]string{
			"Server":            "MXCallMonitor/1.0",
			"X-API-Version":     "1.0",
			"X-Service-Version": version,
		},
		Logger: log.WithField("type", "http"),
	}
	mux.Handle("GET", "/", service.CallMonitor) // страница с мониторингом звонков
	mux.Handle("POST", "/", service.MakeCall)   // сделать звонок
	mux.Handle("GET", "/"+filepath.Base(htmlFile), rest.Redirect("/"))
	mux.Handle("GET", "/*file", rest.Files(filepath.Dir(htmlFile)))
	// инициализируем HTTP сервер
	var server = &http.Server{
		Addr:         host,
		Handler:      mux,
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Minute * 5,
	}
	// анализируем порт
	var httphost, port, err = net.SplitHostPort(host)
	if err, ok := err.(*net.AddrError); ok && err.Err == "missing port in address" {
		httphost = err.Addr
	}
	var isIP = (net.ParseIP(httphost) != nil)
	var notLocal = (httphost != "localhost" &&
		!strings.HasSuffix(httphost, ".local") &&
		!isIP)
	var canCert = notLocal && httphost != "" &&
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
			Cache: autocert.DirCache("letsEncript.cache"),
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
			log.WithError(err).Error("http server stoped")
			os.Exit(2)
		}
	}()

	// устанавливаем соединение с MX
	mxaddr.Type = "Server"
	log.WithFields(log.Fields{
		"host":  mxaddr.Host,
		"login": mxaddr.UserName,
		"type":  mxaddr.Type,
	}).Info("connecting to mx")
	mx, err := mxaddr.Client()
	if err != nil {
		log.WithError(err).Error("mx connection error")
		return
	}
	defer mx.Close()

	log.WithFields(log.Fields{
		"total": exts.Len(),
		"exts":  exts.String(),
	}).Info("starting user monitors")
	var monitors = mx.Monitor("DeliveredEvent")
	for _, ext := range exts.List() {
		// присоединяем к монитору данные о номере пользователя
		if err = monitors.Start(ext, ext); err != nil {
			break
		}
	}
	if err != nil {
		log.WithError(err).Error("monitor error")
		return
	}

	// начинаем обработку событий мониторинга
	for event := range monitors.Events {
		// проверяем, что данное событие относится к мониторингу
		ext, ok := event.Data.(string)
		if !ok {
			log.Debug("no ext")
			continue
		}
		// проверяем, что брокер для пользователя поддерживается
		broker, ok := brokers[ext]
		if !ok {
			log.Debug("not monitored")
			continue
		}
		// преобразуем данные и отправляем брокеру
		switch event.Name {
		case "DeliveredEvent":
			var delivered = &MXDelivery{Timestamp: time.Now().Unix()}
			if err := event.Decode(delivered); err != nil {
				log.WithError(err).Error("bad delivery event")
				continue
			}
			// игнорируем, если не указано вызываемое устройство
			if delivered.CalledDevice == "" {
				log.Warning("ignore empty delivery")
				continue
			}
			data, err := json.Marshal(delivered)
			if err != nil {
				log.WithError(err).Error("bad delivery json format")
				continue
			}
			broker.Data("DeliveredEvent", string(data), "")
			log.WithFields(log.Fields{
				"callID": delivered.CallID,
				"from":   delivered.CallingDevice,
				"to":     delivered.CalledDevice,
				"ext":    ext,
			}).Info("incoming call")
		}
	}
}

// MXDelivery описывает структуру события входящего звонка
type MXDelivery struct {
	MonitorCrossRefID     uint64 `xml:"monitorCrossRefID" json:"-"`
	CallID                uint64 `xml:"connection>callID" json:"callId"`
	DeviceID              string `xml:"connection>deviceID" json:"deviceId"`
	GlobalCallID          string `xml:"connection>globalCallID" json:"globalCallId"`
	AlertingDevice        string `xml:"alertingDevice>deviceIdentifier" json:"alertingDevice"`
	CallingDevice         string `xml:"callingDevice>deviceIdentifier" json:"callingDevice"`
	CalledDevice          string `xml:"calledDevice>deviceIdentifier" json:"calledDevice"`
	LastRedirectionDevice string `xml:"lastRedirectionDevice>deviceIdentifier" json:"lastRedirectionDevice"`
	LocalConnectionInfo   string `xml:"localConnectionInfo" json:"localConnectionInfo"`
	Cause                 string `xml:"cause" json:"cause"`
	CallTypeFlags         uint32 `xml:"callTypeFlags" json:"callTypeFlags,omitempty"`
	Cads                  []struct {
		Name  string `xml:"name,attr" json:"name"`
		Type  string `xml:"type,attr" json:"type"`
		Value string `xml:",chardata" json:"value,omitempty"`
	} `xml:"cad,omitempty" json:"cads,omitempty"`
	Timestamp int64 `xml:"-" json:"time"`
}
