package main

import (
	"encoding/json"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/mdigger/log"
	"golang.org/x/crypto/bcrypt"
)

// Config описывает информацию о конфигурации сервиса.
type Config struct {
	Admin struct {
		Login    string
		Password []byte
	}
	Server struct {
		Host     string
		LogLevel int8
	}
	MX struct {
		Host     string
		Login    string
		Password []byte
	}
	Params   map[string]string
	filename string
	err      error
	mu       sync.RWMutex
}

// LoadConfig загружает конфигурацию из файла.
func LoadConfig(filename string) (*Config, error) {
	var config = new(Config)
	// загружаем конфигурационный файл
	file, err := os.Open(filename)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		err = json.NewDecoder(file).Decode(config)
		file.Close()
		if err != nil {
			return nil, err
		}
	}
	// устанавливаем обязательные значения по умолчанию
	if config.Admin.Login == "" {
		config.Admin.Login = "Administrator"
	}
	if len(config.Admin.Password) == 0 {
		password, err := bcrypt.GenerateFromPassword(
			[]byte(lowerAppName+"adm"), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		config.Admin.Password = password
	}
	if config.Server.Host == "" {
		config.Server.Host = "localhost:8080"
	}
	if level := config.Server.LogLevel; level < 0 {
		log.SetLevel(log.TRACE)
	} else if level > 0 {
		log.SetLevel(log.WARN)
	} else {
		log.SetLevel(log.INFO)
	}
	if len(config.Params) == 0 {
		config.Params = map[string]string{"phoneCountry": "EE"}
	}
	config.filename = filename
	return config, nil
}

// Save сохраняет конфигурационный файл.
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	file, err := os.Create(c.filename)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(file)
	enc.SetIndent("", "\t")
	err = enc.Encode(c)
	if err := file.Close(); err != nil {
		return err
	}
	return err
}

// LogExists возвращает true, если каталог с файлами логов существует.
func (c *Config) LogExists() bool {
	_, err := os.Stat(logPath)
	return err == nil
}

// Version возвращает строку с названием и версией сервера.
func (c *Config) Version() string {
	return agent
}

func (c *Config) Error() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.err != nil {
		return c.err.Error()
	}
	return ""
}

// ServerURL возвращает строку с адресом сервера.
func (c *Config) ServerURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return hostURL(c.Server.Host)
}

// hostURL разбирает разные варианты задания имени и, возможно, порта и
// возвращает строку с адресом для доступа к веб серверу. Протокол HTTPS
// используется только в том случае, если для данного имени можно получить
// сертификат.
func hostURL(host string) string {
	host, port, err := net.SplitHostPort(host)
	if err, ok := err.(*net.AddrError); ok && err.Err == "missing port in address" {
		host = err.Addr
	}
	if host == "" {
		host = "localhost"
	}
	var scheme = "http"
	if (host != "localhost" &&
		!strings.HasSuffix(host, ".local") &&
		!(net.ParseIP(host) != nil)) && host != "" &&
		(port == "443" || port == "") {
		if _, err := net.LookupIP(host); err == nil {
			scheme += "s"
		}
	} else if port != "" && port != "80" && port != "http" && port != "https" {
		host = net.JoinHostPort(host, port)
	}
	return scheme + "://" + host
}
