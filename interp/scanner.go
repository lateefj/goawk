package interp

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
)

type textScanner struct {
	input   io.Reader
	scanner *bufio.Scanner
}

func (ts *textScanner) next(p *interp) error {
	ts.currentError = nil
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
				argvArray := p.arrays[p.getArrayIndex(ScopeGlobal, argvIndex)]
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
						ts.currentError = err
					}
					p.input = input
					p.setFile(filename)
					p.hadFiles = true
				}
			}
			ts.scanner = ts.newScanner(ts.input)
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
}
