// Input/output handling for GoAWK interpreter

package interp

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"
	"strings"

	. "github.com/benhoyt/goawk/internal/ast"
	. "github.com/benhoyt/goawk/lexer"
)

// Print a line of output followed by a newline
func (p *interp) printLine(writer io.Writer, line string) error {
	err := writeOutput(writer, line)
	if err != nil {
		return err
	}
	return writeOutput(writer, p.outputRecordSep)
}

// Determine the output stream for given redirect token and
// destination (file or pipe name)
func (p *interp) getOutputStream(redirect Token, dest Expr) (io.Writer, error) {
	if redirect == ILLEGAL {
		// "ILLEGAL" means send to standard output
		return p.output, nil
	}

	destValue, err := p.eval(dest)
	if err != nil {
		return nil, err
	}
	name := p.toString(destValue)
	if s, ok := p.streams[name]; ok {
		if w, ok := s.(io.Writer); ok {
			return w, nil
		}
		return nil, newError("can't write to reader stream")
	}

	switch redirect {
	case GREATER, APPEND:
		// Write or append to file
		flags := os.O_CREATE | os.O_WRONLY
		if redirect == GREATER {
			flags |= os.O_TRUNC
		} else {
			flags |= os.O_APPEND
		}
		// TODO: this is slow, need to buffer it!
		w, err := os.OpenFile(name, flags, 0644)
		if err != nil {
			return nil, newError("output redirection error: %s", err)
		}
		p.streams[name] = w
		return w, nil

	case PIPE:
		// Pipe to command
		cmd := exec.Command("sh", "-c", name)
		w, err := cmd.StdinPipe()
		if err != nil {
			return nil, newError("error connecting to stdin pipe: %v", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, newError("error connecting to stdout pipe: %v", err)
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return nil, newError("error connecting to stderr pipe: %v", err)
		}
		err = cmd.Start()
		if err != nil {
			fmt.Fprintln(p.errorOutput, err)
			return ioutil.Discard, nil
		}
		go func() {
			io.Copy(p.output, stdout)
		}()
		go func() {
			io.Copy(p.errorOutput, stderr)
		}()
		p.commands[name] = cmd
		p.streams[name] = w
		return w, nil

	default:
		// Should never happen
		panic(fmt.Sprintf("unexpected redirect type %s", redirect))
	}
}

// Get input Scanner to use for "getline" based on file or pipe name
// TODO: this is basically two different functions switching on isFile -- split?
func (p *interp) getInputScanner(name string, isFile bool) (*bufio.Scanner, error) {
	if s, ok := p.streams[name]; ok {
		if _, ok := s.(io.Reader); ok {
			return p.scanners[name], nil
		}
		return nil, newError("can't read from writer stream")
	}
	if isFile {
		r, err := os.Open(name)
		if err != nil {
			return nil, newError("input redirection error: %s", err)
		}
		scanner := p.newScanner(r)
		p.scanners[name] = scanner
		p.streams[name] = r
		return scanner, nil
	} else {
		cmd := exec.Command("sh", "-c", name)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, newError("error connecting to stdin pipe: %v", err)
		}
		r, err := cmd.StdoutPipe()
		if err != nil {
			return nil, newError("error connecting to stdout pipe: %v", err)
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return nil, newError("error connecting to stderr pipe: %v", err)
		}
		err = cmd.Start()
		if err != nil {
			fmt.Fprintln(p.errorOutput, err)
			return bufio.NewScanner(strings.NewReader("")), nil
		}
		go func() {
			io.Copy(stdin, p.stdin)
			stdin.Close()
		}()
		go func() {
			io.Copy(p.errorOutput, stderr)
		}()
		scanner := p.newScanner(r)
		p.commands[name] = cmd
		p.streams[name] = r
		p.scanners[name] = scanner
		return scanner, nil
	}
}

// Create a new buffered Scanner for reading input records
func (p *interp) newScanner(input io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(input)
	switch p.recordSep {
	case "\n":
		// Scanner default is to split on newlines
	case "":
		// Empty string for RS means split on newline and skip blank lines
		scanner.Split(scanLinesSkipBlank)
	default:
		splitter := byteSplitter{p.recordSep[0]}
		scanner.Split(splitter.scan)
	}
	buffer := make([]byte, inputBufSize)
	scanner.Buffer(buffer, maxRecordLength)
	return scanner
}

// Copied from bufio/scan.go in the stdlib: I guess it's a bit more
// efficient than bytes.TrimSuffix(data, []byte("\r"))
func dropCR(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\r' {
		return data[0 : len(data)-1]
	}
	return data
}

// Copied from bufio/scan.go in the standard library
func scanLinesSkipBlank(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		// Skip additional newlines
		j := i + 1
		for j < len(data) && (data[j] == '\n' || data[j] == '\r') {
			j++
		}
		// We have a full newline-terminated line.
		return j, dropCR(data[0:i]), nil
	}
	// If we're at EOF, we have a final, non-terminated line. Return it.
	if atEOF {
		return len(data), dropCR(data), nil
	}
	// Request more data.
	return 0, nil, nil
}

// Splitter function that splits records on the given separator byte
type byteSplitter struct {
	sep byte
}

func (s byteSplitter) scan(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, s.sep); i >= 0 {
		// We have a full sep-terminated record
		return i + 1, data[0:i], nil
	}
	// If at EOF, we have a final, non-terminated record; return it
	if atEOF {
		return len(data), data, nil
	}
	// Request more data
	return 0, nil, nil
}

// Setup for a new input file with given name (empty string if stdin)
func (p *interp) setFile(filename string) {
	p.filename = filename
	p.fileLineNum = 0
}

// Setup for a new input line, and parse it into fields
func (p *interp) setLine(line string) {
	p.line = line
	if p.fieldSep == " " {
		p.fields = strings.Fields(line)
	} else if line == "" {
		p.fields = nil
	} else {
		p.fields = p.fieldSepRegex.Split(line, -1)
	}
	p.numFields = len(p.fields)
}

// Fetch next line (record) of input from current input file, opening
// next input file if done with previous one
func (p *interp) nextLine() (string, error) {
	for {
		if p.scanner == nil {
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
					return "", io.EOF
				}
				// Fetch next filename from ARGV
				index := strconv.Itoa(p.filenameIndex)
				argvIndex := p.program.Arrays["ARGV"]
				filename := p.toString(p.getArrayValue(ScopeGlobal, argvIndex, index))
				p.filenameIndex++

				// Is it actually a var=value assignment?
				matches := varRegex.FindStringSubmatch(filename)
				if len(matches) >= 3 {
					// Yep, set variable to value and keep going
					err := p.setVarByName(matches[1], matches[2])
					if err != nil {
						return "", err
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
					input, err := os.Open(filename)
					if err != nil {
						return "", err
					}
					p.input = input
					p.setFile(filename)
					p.hadFiles = true
				}
			}
			p.scanner = p.newScanner(p.input)
		}
		if p.scanner.Scan() {
			// We scanned some input, break and return it
			break
		}
		if err := p.scanner.Err(); err != nil {
			return "", fmt.Errorf("error reading from input: %s", err)
		}
		// Signal loop to move onto next file
		p.scanner = nil
	}

	// Got a line (record) of input, return it
	p.lineNum++
	p.fileLineNum++
	return p.scanner.Text(), nil
}

// Write output string to given writer, producing correct line endings
// on Windows (CR LF)
func writeOutput(w io.Writer, s string) error {
	if crlfNewline {
		// First normalize to \n, then convert all newlines to \r\n (on Windows)
		// TODO: creating two new strings is almost certainly slow, better to create a custom Writer
		s = strings.Replace(s, "\r\n", "\n", -1)
		s = strings.Replace(s, "\n", "\r\n", -1)
	}
	_, err := io.WriteString(w, s)
	return err
}

// Close all streams, commands, etc (after program execution)
func (p *interp) closeAll() {
	if prevInput, ok := p.input.(io.Closer); ok {
		prevInput.Close()
	}
	for _, w := range p.streams {
		_ = w.Close()
	}
	for _, cmd := range p.commands {
		_ = cmd.Wait()
	}
	if p.flushOutput {
		p.output.(*bufio.Writer).Flush()
	}
	if p.flushError {
		p.errorOutput.(*bufio.Writer).Flush()
	}
}