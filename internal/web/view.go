package web

import (
	"fmt"
	"html/template"
	"time"
)

func funcMap() template.FuncMap {
	return template.FuncMap{
		"bytes": fmtBytes,
		"rate":  fmtRate,
		"speed": fmtSpeed,
		"ratio": fmtRatio,
		"pct":   fmtPct,
		"when":  fmtTime,
		"kib":   fmtKiB,
		"limit": limitText,
	}
}

// fmtKiB is a limit in bytes per second as a KiB/s form value, empty for
// unlimited.
func fmtKiB(bytesPerSec int64) string {
	if bytesPerSec <= 0 {
		return ""
	}
	return fmt.Sprint(bytesPerSec / 1024)
}

// fmtBytes formats a size with binary units, one decimal below 100.
func fmtBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	f := float64(n)
	i := -1
	for f >= unit && i < len(units)-1 {
		f /= unit
		i++
	}
	if f >= 100 {
		return fmt.Sprintf("%.0f %s", f, units[i])
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// fmtRate formats a transfer rate, leaving zero blank so an idle table
// stays quiet.
func fmtRate(n int64) string {
	if n <= 0 {
		return ""
	}
	return fmtBytes(n) + "/s"
}

// fmtSpeed formats a transfer rate, zero included.
func fmtSpeed(n int64) string {
	if n <= 0 {
		return "0 B/s"
	}
	return fmtBytes(n) + "/s"
}

func fmtRatio(r float64) string {
	return fmt.Sprintf("%.2f", r)
}

func fmtPct(p float64) string {
	if p >= 100 {
		return "100%"
	}
	return fmt.Sprintf("%.1f%%", p)
}

// fmtTime formats a timestamp in the server's time zone (TZ), or "" for
// the zero time.
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
