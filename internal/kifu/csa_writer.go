// Package kifu writes shogi game records. v1 supports CSA v2.2 format only.
package kifu

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Record carries the fields needed to emit a .csa file.
type Record struct {
	GameID        string
	BlackName     string
	WhiteName     string
	StartedAt     time.Time
	EndedAt       time.Time
	TimeLimitSec  int64 // Total_Time × TimeUnit, in seconds
	ByoyomiSec    int64
	InitialCSAPos string   // CSA position block (the Game_Summary Position body)
	MoveLines     []string // e.g. "+7776FU,T10" + optional "'* 42 7g7f 3c3d"
	// MoveTimestamps is optional; when present and non-zero at index i it
	// is emitted as an "'$TIME:<rfc3339>" comment immediately after the
	// move line at MoveLines[i]. Length may be shorter than MoveLines
	// (trailing terminator entries like "#RESIGN" get no timestamp).
	MoveTimestamps []time.Time
	Terminator     string   // "%TORYO", "%KACHI", "#RESIGN", "#WIN", ...
	Comments       []string // optional top-of-file "'" comments
	ReturnCode     string   // "\n" (default) or "\r\n"
}

// DefaultReturnCode is the platform-normal line ending.
const DefaultReturnCode = "\n"

// FormatCSA renders r as a CSA v2.2 record.
func FormatCSA(r Record) []byte {
	rc := r.ReturnCode
	if rc == "" {
		rc = DefaultReturnCode
	}
	var b bytes.Buffer
	write := func(s string) {
		b.WriteString(s)
		b.WriteString(rc)
	}
	write("V2.2")
	for _, c := range r.Comments {
		write("'" + c)
	}
	if r.BlackName != "" {
		write("N+" + r.BlackName)
	}
	if r.WhiteName != "" {
		write("N-" + r.WhiteName)
	}
	if r.GameID != "" {
		write("$EVENT:" + r.GameID)
	}
	if !r.StartedAt.IsZero() {
		write("$START_TIME:" + r.StartedAt.Format("2006/01/02 15:04:05"))
	}
	if r.TimeLimitSec > 0 {
		h := r.TimeLimitSec / 3600
		m := (r.TimeLimitSec % 3600) / 60
		write(fmt.Sprintf("$TIME_LIMIT:%02d:%02d+%02d", h, m, r.ByoyomiSec))
	}
	// Initial position block: trust the CSA input verbatim except ensure a
	// trailing newline is not double-written by our rc pass.
	ipos := strings.TrimRight(r.InitialCSAPos, "\n")
	for _, line := range strings.Split(ipos, "\n") {
		write(line)
	}
	for i, ml := range r.MoveLines {
		write(ml)
		if i < len(r.MoveTimestamps) && !r.MoveTimestamps[i].IsZero() {
			write("'$TIME:" + r.MoveTimestamps[i].Format(time.RFC3339))
		}
	}
	if r.Terminator != "" {
		write(r.Terminator)
	}
	if !r.EndedAt.IsZero() {
		write("$END_TIME:" + r.EndedAt.Format("2006/01/02 15:04:05"))
	}
	return b.Bytes()
}

// Write writes r to dir using template (see RenderFilename) and returns
// the final path. Creates dir if missing.
func Write(dir, template string, r Record) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := RenderFilename(template, r) + ".csa"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, FormatCSA(r), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// RenderFilename expands placeholders. Supported tokens:
//
//	{datetime}   — StartedAt as YYYYMMDD_hhmmss (now if zero)
//	{_title}     — "_" + GameID when set, else ""
//	{_sente}     — "_" + BlackName when set, else ""
//	{_gote}      — "_" + WhiteName when set, else ""
//	{sente}      — BlackName
//	{gote}       — WhiteName
//	{title}      — GameID
//
// Characters illegal on common filesystems (/\\:*?"<>|) are replaced with '_'.
func RenderFilename(template string, r Record) string {
	if template == "" {
		template = "{datetime}{_title}{_sente}{_gote}"
	}
	start := r.StartedAt
	if start.IsZero() {
		start = time.Now()
	}
	repl := map[string]string{
		"{datetime}": start.Format("20060102_150405"),
		"{_title}":   opt("_", r.GameID),
		"{_sente}":   opt("_", r.BlackName),
		"{_gote}":    opt("_", r.WhiteName),
		"{sente}":    r.BlackName,
		"{gote}":     r.WhiteName,
		"{title}":    r.GameID,
	}
	out := template
	for k, v := range repl {
		out = strings.ReplaceAll(out, k, v)
	}
	return sanitizeFilename(out)
}

func opt(prefix, v string) string {
	if v == "" {
		return ""
	}
	return prefix + v
}

func sanitizeFilename(s string) string {
	bad := `/\:*?"<>|`
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(bad, c) >= 0 || c < 0x20 {
			out = append(out, '_')
		} else {
			out = append(out, c)
		}
	}
	return string(out)
}
