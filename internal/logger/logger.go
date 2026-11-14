package logger

import (
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog"
)

const (
	colorBlack = iota + 30
	colorRed
	colorGreen
	colorYellow
	colorBlue
	colorMagenta
	colorCyan
	colorWhite

	colorBold     = 1
	colorDarkGray = 90
)

func colorize(s interface{}, c int) string {
	return fmt.Sprintf("\x1b[%dm%v\x1b[0m", c, s)
}

// New creates a logger based on the ENV environment variable
func New() zerolog.Logger {
	env := os.Getenv("ENV")

	if env == "development" || env == "dev" || env == "" {
		return NewDevelopment()
	}
	return NewProduction()
}

// NewDevelopment creates a development logger with console output and colors
func NewDevelopment() zerolog.Logger {
	output := zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "2006-01-02 15:04:05",
		FormatLevel: func(i interface{}) string {
			var l string
			if ll, ok := i.(string); ok {
				switch ll {
				case "trace":
					l = colorize("TRC", colorMagenta)
				case "debug":
					l = colorize("DBG", colorYellow)
				case "info":
					l = colorize("INF", colorGreen)
				case "warn":
					l = colorize("WRN", colorRed)
				case "error":
					l = colorize("ERR", colorRed)
				case "fatal":
					l = colorize("FTL", colorRed)
				case "panic":
					l = colorize("PNC", colorRed)
				default:
					l = colorize(strings.ToUpper(ll)[0:3], colorBold)
				}
			} else {
				l = strings.ToUpper(fmt.Sprintf("%s", i))[0:3]
			}
			return l
		},
	}
	return zerolog.New(output).With().Timestamp().Logger()
}

// NewProduction creates a production logger with JSON output and UNIX timestamps
func NewProduction() zerolog.Logger {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	return zerolog.New(privacyWriter{os.Stderr}).Level(zerolog.InfoLevel).With().Timestamp().Logger()
}
