package contacts

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	MaxImportRows  = 50000
	MaxImportBytes = 20 << 20
)

var (
	ErrNotUTF8     = errors.New("file is not valid UTF-8")
	ErrTooManyRows = errors.New("file has more than 50000 rows")
	ErrEmptyCSV    = errors.New("file has no header row and at least one data row")
	ErrBadCSV      = errors.New("file is not valid CSV")
)

// SafeCell prefixes cells that a spreadsheet would run as a formula, so an exported name like
// "=HYPERLINK(...)" stays text (OWASP CSV injection).
func SafeCell(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// CSVWriter writes rows with formula-injection protection and a UTF-8 byte order mark so
// Excel opens the file as UTF-8. It uses ";" as Dutch spreadsheets expect.
type CSVWriter struct {
	w *csv.Writer
}

func NewCSVWriter(w io.Writer) (*CSVWriter, error) {
	if _, err := w.Write([]byte("\xEF\xBB\xBF")); err != nil {
		return nil, fmt.Errorf("write csv: %w", err)
	}
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	return &CSVWriter{w: cw}, nil
}

func (c *CSVWriter) Write(cells ...string) error {
	safe := make([]string, len(cells))
	for i, cell := range cells {
		safe[i] = SafeCell(cell)
	}
	if err := c.w.Write(safe); err != nil {
		return fmt.Errorf("write csv: %w", err)
	}
	return nil
}

func (c *CSVWriter) Flush() error {
	c.w.Flush()
	return c.w.Error()
}

// Table is a parsed CSV file: a header row and data rows.
type Table struct {
	Delimiter string
	Header    []string
	Rows      [][]string
}

// DetectDelimiter picks ";" or "," by counting unquoted occurrences in the first line.
func DetectDelimiter(data []byte) string {
	semi, comma := 0, 0
	inQuotes := false
loop:
	for _, b := range data {
		switch b {
		case '"':
			inQuotes = !inQuotes
		case ';':
			if !inQuotes {
				semi++
			}
		case ',':
			if !inQuotes {
				comma++
			}
		case '\n':
			if !inQuotes {
				break loop
			}
		}
	}
	if semi > comma {
		return ";"
	}
	return ","
}

// ParseCSV reads a UTF-8 file with a header row. delimiter is "," or ";", or "" to detect it.
// Fully blank lines are skipped; rows may be shorter or longer than the header.
func ParseCSV(data []byte, delimiter string) (Table, error) {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if !utf8.Valid(data) {
		return Table{}, ErrNotUTF8
	}
	if delimiter == "" {
		delimiter = DetectDelimiter(data)
	}
	if delimiter != "," && delimiter != ";" {
		return Table{}, errors.New("delimiter must be , or ;")
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = rune(delimiter[0])
	r.FieldsPerRecord = -1
	r.ReuseRecord = false

	t := Table{Delimiter: delimiter}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Table{}, fmt.Errorf("%w: %w", ErrBadCSV, err)
		}
		if isBlank(rec) {
			continue
		}
		if t.Header == nil {
			t.Header = trimCells(rec)
			continue
		}
		if len(t.Rows) >= MaxImportRows {
			return Table{}, ErrTooManyRows
		}
		t.Rows = append(t.Rows, rec)
	}
	if t.Header == nil || len(t.Rows) == 0 {
		return Table{}, ErrEmptyCSV
	}
	return t, nil
}

func isBlank(rec []string) bool {
	for _, c := range rec {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func trimCells(rec []string) []string {
	out := make([]string, len(rec))
	for i, c := range rec {
		out[i] = strings.TrimSpace(c)
	}
	return out
}
