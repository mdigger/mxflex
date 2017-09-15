package main

import (
	"errors"
	"io/ioutil"
	"net"

	"github.com/BurntSushi/toml"
)

// Config описывает формат конфигурационного файла.
type Config struct {
	Host string
	MX   struct {
		Addr     string
		Login    string
		Password string
	}
}

// LoadConfig загружает конфигурационный файл.
func LoadConfig(filename string) (*Config, error) {
	data, err := ioutil.ReadFile(configName)
	if err != nil {
		return nil, err
	}
	var config = new(Config)
	if err = toml.Unmarshal(data, config); err != nil {
		return nil, err
	}
	if config.Host == "" {
		config.Host = "localhost:8080"
	}
	_, _, err = net.SplitHostPort(config.MX.Addr)
	if err, ok := err.(*net.AddrError); ok && err.Err == "missing port in address" {
		config.MX.Addr = net.JoinHostPort(config.MX.Addr, "7778")
	}
	if config.MX.Login == "" {
		return nil, errors.New("mx login is empty")
	}
	return config, nil
}
