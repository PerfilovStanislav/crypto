package config

import "github.com/ilyakaznacheev/cleanenv"

type Config struct {
	Log  LogConfig  `yaml:"log" env-prefix:"LOG_"`
	Db   DbConfig   `yaml:"db" env-prefix:"DB_"`
	Http HttpConfig `yaml:"http" env-prefix:"HTTP_"`
}

type LogConfig struct {
	Level string `yaml:"level" env:"LEVEL" env-default:"info"`
	Path  string `yaml:"path" env:"PATH" env-default:"./logs/cnd.log"`
	Days  int    `yaml:"days" env-default:"1"`
	Size  int    `yaml:"size" env-default:"100"`
}

type DbConfig struct {
	Host     string `yaml:"host" env:"HOST" env-default:"localhost"`
	Port     int    `yaml:"port" env:"PORT" env-default:"5432"`
	User     string `yaml:"user" env:"USER" env-default:"appuser"`
	Password string `yaml:"password" env:"PASSWORD" env-default:"qwerty12"`
	Database string `yaml:"database" env:"DATABASE" env-default:"mobstra"`
}

type HttpConfig struct {
	Port int `yaml:"port" env:"PORT" env-default:"8888"`
}

func Init() *Config {
	var config Config

	err := cleanenv.ReadConfig("config.yaml", &config)
	if err != nil {
		panic(err)
	}

	return &config
}
