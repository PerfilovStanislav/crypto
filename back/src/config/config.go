package config

import (
	"errors"
	"fmt"
	"indicator"
	"math"
	"os"
	"path/filepath"
	"source"

	"github.com/ilyakaznacheev/cleanenv"
)

type Config struct {
	Log      LogConfig      `yaml:"log" env-prefix:"LOG_"`
	Db       DbConfig       `yaml:"db" env-prefix:"DB_"`
	Http     HttpConfig     `yaml:"http" env-prefix:"HTTP_"`
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

type HttpConfig struct {
	Port   int  `yaml:"port" env:"PORT" env-default:"8888"`
	Active bool `yaml:"active" env:"ACTIVE"`
}

type AnalyzerConfig struct {
	Active               bool                `yaml:"active" env:"ACTIVE"` // запускать перебор стратегий
	Threads              int                 `yaml:"threads" env:"THREADS"`
	Pair                 string              `yaml:"pair" env:"PAIR"`
	Direction            string              `yaml:"direction" env:"DIRECTION" env-default:"buy"` // конфиг пары: <direction>/<PAIR>.yaml
	Timeframe            string              `yaml:"timeframe" env:"TIMEFRAME"`
	Commission           float64             `yaml:"commission" env:"COMMISSION"`
	Decimals             int                 `yaml:"decimals" env:"DECIMALS"`
	MinCloses            int                 `yaml:"minCloses" env:"MIN_CLOSES"`
	MinSignals           int                 `yaml:"minSignals" env:"MIN_SIGNALS"`
	MinSignalsLastMonths int                 `yaml:"minSignalsLastMonths" env:"MIN_SIGNALS_LAST_MONTHS"`
	LastMonthsLimit      int                 `yaml:"lastMonthsLimit" env:"LAST_MONTHS_LIMIT"`
	MinTakeProfits       int                 `yaml:"minTakeProfits" env:"MIN_TAKE_PROFITS"`
	Takeprofit           RangeConfig         `yaml:"takeprofit" env:"TAKEPROFIT"`
	Stoploss             RangeConfig         `yaml:"stoploss" env:"STOPLOSS"`
	Indicators           [][]IndicatorConfig `yaml:"indicators" env-prefix:"INDICATORS_"`
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

// Dir возвращает каталог с config.yaml и <direction>/<PAIR>.yaml: CONFIG_DIR, а если он не задан —
// ближайший каталог configs/ с config.yaml вверх от текущего.
func Dir() (string, error) {
	if dir := os.Getenv("CONFIG_DIR"); dir != "" {
		return dir, nil
	}

	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := wd; ; {
		candidate := filepath.Join(d, "configs")
		if st, err := os.Stat(filepath.Join(candidate, "config.yaml")); err == nil && !st.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", errors.New("configs/config.yaml not found in " + wd + " or its parents; set CONFIG_DIR")
		}
		d = parent
	}
}

func Init() *Config {
	var config Config

	dir, err := Dir()
	if err != nil {
		panic(err)
	}

	err = cleanenv.ReadConfig(filepath.Join(dir, "config.yaml"), &config)
	if err != nil {
		panic(err)
	}

	if d := config.Analyzer.Direction; d != "buy" && d != "sell" {
		panic(fmt.Sprintf("analyzer.direction must be buy or sell, got %q", d))
	}

	if config.Analyzer.Pair != "" {
		pairConfigPath := filepath.Join(dir, config.Analyzer.Direction, config.Analyzer.Pair+".yaml")
		err = cleanenv.ReadConfig(pairConfigPath, &config)
		if err != nil {
			panic(err)
		}
	}

	if config.Analyzer.Decimals > 0 {
		factor := math.Pow10(config.Analyzer.Decimals)
		config.Analyzer.Takeprofit.Start /= factor
		config.Analyzer.Takeprofit.End /= factor
		config.Analyzer.Takeprofit.Step /= factor

		config.Analyzer.Stoploss.Start /= factor
		config.Analyzer.Stoploss.End /= factor
		config.Analyzer.Stoploss.Step /= factor
	}

	return &config
}
