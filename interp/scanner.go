package interp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/benhoyt/goawk/internal/ast"
)

// Provide way to populate different data formats with different contexts
type dataScanner interface {
	//scan() bool
	// Get the next data record and populate the proper context
	next(*interp) error
}

type textScanner struct {
	scanner *bufio.Scanner
}

func initTextScanner(rc RecordSeperator, input io.Reader) (*bufio.Scanner, error) {
	if input == nil {
		return nil, fmt.Errorf("textScanner input reader can not be nil")
	}
	scanner := bufio.NewScanner(input)
	switch RecordSeperator(rc) {
	case RecordSeperatorNewLine:
		// Scanner default is to split on newlines
	case RecordSeperatorEmpty:
		// Empty string for RS means split on \n\n (blank lines)
		scanner.Split(scanLinesBlank)
	default:
		splitter := byteSplitter{rc[0]}
		scanner.Split(splitter.scan)
	}
	buffer := make([]byte, inputBufSize)
	scanner.Buffer(buffer, maxRecordLength)
	return scanner, nil
}
func newTextScanner(rc RecordSeperator, input io.Reader) (*textScanner, error) {
	scanner, err := initTextScanner(rc, input)
	if err != nil {
		return nil, err
	}
	return &textScanner{scanner}, nil
}

func (ts *textScanner) next(p *interp) error {
	for {
		if ts.scanner == nil {
			if prevInput, ok := p.input.(io.Closer); ok && p.input != p.stdin {
				// Previous input is file, close it
				prevInput.Close()
			}
			if p.filenameIndex >= p.argc && !p.hadFiles {
				// Moved past number of ARGV args and haven't seen
				// any files yet, use stdin
				p.input = p.stdin
				p.setFile("")
				p.hadFiles = true
			} else {
				if p.filenameIndex >= p.argc {
					// Done with ARGV args, all done with input
					return io.EOF
				}
				// Fetch next filename from ARGV. Can't use
				// getArrayValue() here as it would set the value if
				// not present
				index := strconv.Itoa(p.filenameIndex)
				argvIndex := p.program.Arrays["ARGV"]
				argvArray := p.arrays[p.getArrayIndex(ast.ScopeGlobal, argvIndex)]
				filename := p.toString(argvArray[index])
				p.filenameIndex++

				// Is it actually a var=value assignment?
				matches := varRegex.FindStringSubmatch(filename)
				if len(matches) >= 3 {
					// Yep, set variable to value and keep going
					err := p.setVarByName(matches[1], matches[2])
					if err != nil {
						return err
					}
					continue
				} else if filename == "" {
					// ARGV arg is empty string, skip
					p.input = nil
					continue
				} else if filename == "-" {
					// ARGV arg is "-" meaning stdin
					p.input = p.stdin
					p.setFile("")
				} else {
					// A regular file name, open it
					if p.noFileReads {
						return newError("can't read from file due to NoFileReads")
					}
					input, err := os.Open(filename)
					if err != nil {
						return err
					}
					p.input = input
					p.setFile(filename)
					p.hadFiles = true
				}
			}
			var err error
			ts.scanner, err = initTextScanner(p.recordSep, p.input)
			if err != nil {
				return err
			}
		}
		if ts.scanner.Scan() {
			// We scanned some input, break and return it
			break
		}
		if err := ts.scanner.Err(); err != nil {
			return fmt.Errorf("error reading from input: %s", err)
		}
		// Signal loop to move onto next file
		ts.scanner = nil
	}

	// Got a line (record) of input, return it
	p.lineNum++
	p.fileLineNum++
	p.setLine(ts.scanner.Text())
	return nil
}

type jsonScanner struct {
	decoder *json.Decoder
}

func newJsonScanner(input io.Reader) (*jsonScanner, error) {
	if input == nil {
		return nil, fmt.Errorf("Json Scanner input can not be nil")
	}
	dec := json.NewDecoder(input)
	return &jsonScanner{dec}, nil

}
func (js *jsonScanner) next(p *interp) error {
	var v interface{}
	err := js.decoder.Decode(&v)
	if err != nil {
		return err
	}
	if v == nil {
		p.jsonPayload = nil
		return nil
	}
	// Convert to map of string interface
	m := v.(map[string]interface{})
	p.jsonPayload = m

	return nil
}
func newDataScanner(rc RecordSeperator, input io.Reader) (dataScanner, error) {
	switch rc {
	case RecordSeperatorJson:
		return newJsonScanner(input)
	default:
		return newTextScanner(rc, input)
	}
}
