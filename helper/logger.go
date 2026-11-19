package main

import (
	"fmt"
	"log"
	"strings"

	librespot "github.com/devgianlu/go-librespot"
)

type logger struct {
	fields []string
}

func (l logger) add(key string, value any) logger {
	fields := append([]string(nil), l.fields...)
	fields = append(fields, fmt.Sprintf("%s=%v", key, value))
	return logger{fields: fields}
}

func (l logger) write(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if len(l.fields) > 0 {
		msg = "[" + strings.Join(l.fields, " ") + "] " + msg
	}
	log.Printf("%s %s", level, msg)
}

func (l logger) Tracef(format string, args ...any) { l.write("TRACE", format, args...) }
func (l logger) Debugf(format string, args ...any) { l.write("DEBUG", format, args...) }
func (l logger) Infof(format string, args ...any)  { l.write("INFO", format, args...) }
func (l logger) Warnf(format string, args ...any)  { l.write("WARN", format, args...) }
func (l logger) Errorf(format string, args ...any) { l.write("ERROR", format, args...) }

func (l logger) Trace(args ...any) { l.write("TRACE", "%s", fmt.Sprint(args...)) }
func (l logger) Debug(args ...any) { l.write("DEBUG", "%s", fmt.Sprint(args...)) }
func (l logger) Info(args ...any)  { l.write("INFO", "%s", fmt.Sprint(args...)) }
func (l logger) Warn(args ...any)  { l.write("WARN", "%s", fmt.Sprint(args...)) }
func (l logger) Error(args ...any) { l.write("ERROR", "%s", fmt.Sprint(args...)) }

func (l logger) WithField(key string, value any) librespot.Logger {
	return l.add(key, value)
}

func (l logger) WithError(err error) librespot.Logger {
	return l.add("error", err)
}
