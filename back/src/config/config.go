package config

import (
	"indicator"
	"source"

	"github.com/ilyakaznacheev/cleanenv"
)

type Config struct {
	Log      LogConfig      `yaml:"log" env-prefix:"LOG_"`
	Db       DbConfig       `yaml:"db" env-prefix:"DB_"`
	Analyzer AnalyzerConfig `yaml:"analyzer" env-prefix:"ANALYZER_"`
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

type AnalyzerConfig struct {
	Threads    int                 `yaml:"threads" env:"THREADS"`
	Pair       string              `yaml:"pair" env:"PAIR"`
	Timeframe  string              `yaml:"timeframe" env:"TIMEFRAME"`
	MinCloses  int                 `yaml:"minCloses" env:"MIN_CLOSES"`
	MinSignals int                 `yaml:"minSignals" env:"MIN_SIGNALS"`
	Takeprofit RangeConfig         `yaml:"takeprofit" env:"TAKEPROFIT"`
	Stoploss   RangeConfig         `yaml:"stoploss" env:"STOPLOSS"`
	Indicators [][]IndicatorConfig `yaml:"indicators" env-prefix:"INDICATORS_"`
}

type IndicatorConfig struct {
	Type    indicator.Type `yaml:"type" env:"TYPE"`
	Coefs   RangeConfig    `yaml:"coefs" env:"COEFS"`
	Sources []source.Type  `yaml:"sources" env:"SOURCES"`
}

type RangeConfig struct {
	Start float64
	End   float64
	Step  float64
}

func Init() *Config {
	var config Config

	err := cleanenv.ReadConfig("config.yaml", &config)
	if err != nil {
		panic(err)
	}

	return &config
}
