package tool

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/provider"
)

// PDFConfig bounds a single rendering call. Rendering requires Poppler's
// pdfinfo and pdftoppm on PATH. Supply this tool to an image-capable model.
type PDFConfig struct {
	// Dir is the base for relative input paths; absolute paths are used directly.
	Dir           string
	MaxPages      int
	MaxFileBytes  int64
	MaxImageBytes int
	MaxDimension  int
	Timeout       time.Duration
}

type PDF struct {
	config PDFConfig
	bound  Func[pdfArgs]
}
type pdfArgs struct {
	Path  string `json:"path"`
	Pages []int  `json:"pages"`
}

func NewPDF(config PDFConfig) (*PDF, error) {
	var err error
	config.Dir, err = directory(config.Dir)
	if err != nil {
		return nil, err
	}
	config.MaxPages = cmp.Or(config.MaxPages, 6)
	config.MaxFileBytes = cmp.Or(config.MaxFileBytes, 32<<20)
	config.MaxImageBytes = cmp.Or(config.MaxImageBytes, 16<<20)
	config.MaxDimension = cmp.Or(config.MaxDimension, 1600)
	config.Timeout = cmp.Or(config.Timeout, 30*time.Second)
	if config.MaxPages < 1 || config.MaxPages > 32 || config.MaxFileBytes < 1 || config.MaxImageBytes < 1 || config.MaxDimension < 64 || config.MaxDimension > 4096 || config.Timeout <= 0 {
		return nil, errors.New("invalid PDF limits: pages 1..32, dimension 64..4096, positive byte limits and timeout required")
	}
	p := &PDF{config: config}
	params, err := NewParameters[pdfArgs](Nullable("pages", "read the default first pages"), MinLength("path", 1), MinItems("pages", 1), MaxItems("pages", config.MaxPages), Minimum("pages[]", 1))
	if err != nil {
		return nil, err
	}
	p.bound = Func[pdfArgs]{
		Spec: Definition[pdfArgs]{
			Name:        "read_pdf",
			Description: p.description(),
			Parameters:  params,
		},
		Invoke: p.handle,
	}
	return p, nil
}

func (p *PDF) Definition() provider.ToolDefinition { return p.bound.Definition() }
func (p *PDF) Validate() error                     { return p.bound.Validate() }
func (p *PDF) description() string {
	return fmt.Sprintf("Read a PDF by rendering its pages as images for you to inspect. Accepts absolute paths; relative paths resolve from %s. Requires image input support. pages is a nullable list of 1-based page numbers in reading order, at most %d. When null, return the first %d pages. Metadata reports total and omitted page counts; request remaining pages in later calls.", p.config.Dir, p.config.MaxPages, p.config.MaxPages)
}
func (p *PDF) Call(ctx context.Context, call Call) (Result, error) { return p.bound.Call(ctx, call) }

type PDFMetadata struct {
	Path         string `json:"path"`
	TotalPages   int    `json:"total_pages"`
	Pages        []int  `json:"pages"`
	OmittedPages int    `json:"omitted_pages"`
}

func (p *PDF) handle(ctx context.Context, _ Call, args pdfArgs) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	if strings.TrimSpace(args.Path) == "" {
		return Result{}, errors.New("path must name a PDF file")
	}
	path := args.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.config.Dir, path)
	}
	// Reject special files before opening (opening a FIFO can block).
	info, err := os.Stat(path)
	if err != nil {
		return Result{}, err
	}
	if !info.Mode().IsRegular() {
		return Result{}, errors.New("PDF must be a regular file")
	}
	// Snapshot the input so metadata and every rendered page use the same bytes.
	input, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer input.Close()
	info, err = input.Stat()
	if err != nil {
		return Result{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > p.config.MaxFileBytes {
		return Result{}, errors.New("PDF must be a regular file within the byte limit")
	}
	tmp, err := os.MkdirTemp("", "strap-pdf-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmp)
	snapshot := filepath.Join(tmp, "input.pdf")
	file, err := os.OpenFile(snapshot, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return Result{}, err
	}
	n, copyErr := io.Copy(file, io.LimitReader(input, p.config.MaxFileBytes+1))
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return Result{}, err
	}
	if n > p.config.MaxFileBytes {
		return Result{}, errors.New("PDF exceeds the byte limit")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	infoBytes, err := pdfCommand(ctx, 64<<10, "pdfinfo", snapshot)
	if err != nil {
		return Result{}, err
	}
	total := 0
	for line := range strings.SplitSeq(string(infoBytes), "\n") {
		count, found := strings.CutPrefix(line, "Pages:")
		if found {
			total, err = strconv.Atoi(strings.TrimSpace(count))
			break
		}
	}
	if err != nil || total < 1 {
		return Result{}, errors.New("pdfinfo did not return a valid page count")
	}
	if args.Pages == nil {
		for page := 1; page <= min(total, p.config.MaxPages); page++ {
			args.Pages = append(args.Pages, page)
		}
	}
	seen := make(map[int]bool)
	for _, page := range args.Pages {
		if page < 1 || page > total || seen[page] {
			return Result{}, fmt.Errorf("invalid or duplicate page %d; PDF has %d pages", page, total)
		}
		seen[page] = true
	}
	result, err := JSON(PDFMetadata{Path: args.Path, TotalPages: total, Pages: args.Pages, OmittedPages: total - len(args.Pages)})
	if err != nil {
		return Result{}, err
	}
	remaining := p.config.MaxImageBytes
	for _, page := range args.Pages {
		data, err := pdfCommand(ctx, remaining, "pdftoppm", "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), "-singlefile", "-scale-to", strconv.Itoa(p.config.MaxDimension), "-png", snapshot)
		if err != nil {
			return Result{}, fmt.Errorf("render page %d: %w", page, err)
		}
		imageInfo, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || imageInfo.Width < 1 || imageInfo.Height < 1 || imageInfo.Width > p.config.MaxDimension || imageInfo.Height > p.config.MaxDimension {
			return Result{}, fmt.Errorf("renderer returned an invalid or oversized PNG for page %d", page)
		}
		remaining -= len(data)
		result.Content = append(result.Content,
			content.Part{Text: fmt.Sprintf("PDF %s, page %d of %d", args.Path, page, total)},
			content.Part{Image: &content.Image{MIMEType: "image/png", Data: data}},
		)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return result, nil
}

// Bounds retained stdout/stderr without blocking producers on a full buffer.
type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

// Hide bytes.Buffer's ReaderFrom fast path so io.Copy still enforces Write's cap.
func (b *limitedBuffer) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{b}, r)
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	n := len(data)
	remaining := max(0, b.limit-b.Len())
	if n > remaining {
		b.exceeded = true
		data = data[:remaining]
	}
	_, _ = b.Buffer.Write(data)
	return n, nil
}
func pdfCommand(ctx context.Context, limit int, program string, args ...string) ([]byte, error) {
	if limit < 1 {
		return nil, errors.New("PDF image byte limit reached; request fewer pages")
	}
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	stdout, stderr := &limitedBuffer{limit: limit}, &limitedBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 250 * time.Millisecond
	if processGroupsSupported {
		configureProcessGroup(cmd)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s (install Poppler to provide pdfinfo/pdftoppm): %w", program, err)
	}
	if processGroupsSupported {
		defer stopProcessGroup(cmd)
	}
	err := cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", program, err, strings.TrimSpace(stderr.String()))
	}
	if stdout.exceeded {
		return nil, fmt.Errorf("%s output exceeds the byte limit; request fewer pages", program)
	}
	return stdout.Bytes(), nil
}

func (p *PDF) prepare(ctx context.Context, call Call) (func() (Result, error), error) {
	return p.bound.prepare(ctx, call)
}
func (p *PDF) snapshot() preparedTool { return p.bound.snapshot() }

func (p *PDF) contract() *parameterNode { return p.bound.contract() }
