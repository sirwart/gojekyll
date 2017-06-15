package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/acstech/liquid"
	"github.com/russross/blackfriday"
	yaml "gopkg.in/yaml.v2"
)

var (
	frontmatterMatcher             = regexp.MustCompile(`(?s)^---\n(.+?\n)---\n`)
	templateVariableMatcher        = regexp.MustCompile(`:\w+\b`)
	nonAlphanumericSequenceMatcher = regexp.MustCompile(`[^[:alnum:]]+`)
)

var permalinkStyles = map[string]string{
	"date":    "/:categories/:year/:month/:day/:title.html",
	"pretty":  "/:categories/:year/:month/:day/:title/",
	"ordinal": "/:categories/:year/:y_day/:title.html",
	"none":    "/:categories/:title.html",
}

// Page is a Jekyll page.
type Page interface {
	Path() string
	Source() string
	Static() bool
	Published() bool
	Permalink() string
	TemplateObject() VariableMap
	Write(io.Writer) error
	DebugVariables() VariableMap
}

type pageFields struct {
	path        string // this is the relative path
	permalink   string
	published   bool
	FrontMatter VariableMap
}

func (p *pageFields) String() string {
	return fmt.Sprintf("%s{Path=%v, Permalink=%v}",
		reflect.TypeOf(p).Name(), p.path, p.permalink)
}

func (p *pageFields) Path() string      { return p.path }
func (p *pageFields) Permalink() string { return p.permalink }
func (p *pageFields) Published() bool   { return p.published }

// StaticPage is a static page.
type StaticPage struct {
	pageFields
}

// DynamicPage is a static page, that includes frontmatter.
type DynamicPage struct {
	pageFields
	Content []byte
}

// Static returns a bool indicatingthat the page is a not static page.
func (p *DynamicPage) Static() bool { return false }

// Static returns a bool indicating that the page is a static page.
func (p *StaticPage) Static() bool { return true }

// TemplateObject returns metadata for use in the representation of the page as a collection item
func (p *StaticPage) TemplateObject() VariableMap {
	return mergeVariableMaps(p.FrontMatter, p.pageFields.TemplateObject())
}

// TemplateObject returns the attributes of the template page object.
func (p *DynamicPage) TemplateObject() VariableMap {
	data := VariableMap{
		"url":  p.Permalink(),
		"path": p.Source(),
		// TODO content title excerpt date id categories tags next previous
		// TODO Posts should get date, category, categories, tags
		// TODO only do the following if it's a collection document?
		"relative_path": p.Path(),
		// TODO collections: output collection(name) date(of the collection)
	}
	for k, v := range p.FrontMatter {
		switch k {
		case "layout", "permalink", "published":
		default:
			data[k] = v
		}
	}
	return data
}

// TemplateObject returns the attributes of the template page object.
func (p *pageFields) TemplateObject() VariableMap {
	var (
		path = "/" + p.path
		base = filepath.Base(path)
		ext  = filepath.Ext(path)
	)

	return VariableMap{
		"path":          path,
		"modified_time": 0, // TODO
		"name":          base,
		"basename":      base[:len(base)-len(ext)],
		"extname":       ext,
	}
}

// TemplateVariables returns the local variables for template evaluation
func (p *DynamicPage) TemplateVariables() VariableMap {
	return VariableMap{
		"page": p.TemplateObject(),
		"site": site.Variables,
	}
}

// DebugVariables returns a map that's useful to present during diagnostics.
// For a static page, this is just the page's template object attributes.
func (p *pageFields) DebugVariables() VariableMap {
	return p.TemplateObject()
}

// DebugVariables returns a map that's useful to present during diagnostics.
// For a dynamic page, this is the local variable map that is used for template evaluation.
func (p *DynamicPage) DebugVariables() VariableMap {
	return p.TemplateVariables()
}

// ReadPage reads a Page from a file, using defaults as the default front matter.
func ReadPage(path string, defaults VariableMap) (p Page, err error) {
	// TODO don't read, parse binary files
	source, err := ioutil.ReadFile(filepath.Join(site.Source, path))
	if err != nil {
		return nil, err
	}

	if match := frontmatterMatcher.FindSubmatchIndex(source); match != nil {
		p, err = makeDynamicPage(path, defaults, source, match)
	} else {
		p, err = makeStaticPage(path, defaults)
	}
	return
}

func makeDynamicPage(path string, defaults VariableMap, source []byte, match []int) (*DynamicPage, error) {
	// TODO only prepend newlines if it's markdown
	body := append(
		regexp.MustCompile(`[^\n\r]+`).ReplaceAllLiteral(source[:match[1]], []byte{}),
		source[match[1]:]...)

	frontMatter := VariableMap{}
	if err := yaml.Unmarshal(source[match[2]:match[3]], &frontMatter); err != nil {
		err := &os.PathError{Op: "read frontmatter", Path: path, Err: err}
		return nil, err
	}
	frontMatter = mergeVariableMaps(defaults, frontMatter)

	pattern := frontMatter.String("permalink", ":path")
	permalink, err := expandPermalinkPattern(pattern, path, frontMatter)
	if err != nil {
		return nil, err
	}

	p := &DynamicPage{
		pageFields: pageFields{
			path:        path,
			permalink:   permalink,
			published:   frontMatter.Bool("published", true),
			FrontMatter: frontMatter,
		},
		Content: body,
	}
	return p, nil
}

func makeStaticPage(path string, frontMatter VariableMap) (*StaticPage, error) {
	permalink := "/" + path // TODO resolve same as for dynamic page
	p := &StaticPage{
		pageFields: pageFields{
			path:        path,
			permalink:   permalink,
			published:   frontMatter.Bool("published", true),
			FrontMatter: frontMatter,
		},
	}
	return p, nil
}

// Source returns the file path of the page source.
func (p *pageFields) Source() string {
	return filepath.Join(site.Source, p.path)
}

func (p *StaticPage) Write(w io.Writer) error {
	source, err := ioutil.ReadFile(p.Source())
	if err != nil {
		return err
	}
	_, err = w.Write(source)
	return err
}

// Write applies Liquid and Markdown, as appropriate.
func (p *DynamicPage) Write(w io.Writer) error {
	parsingLiquid := true
	defer func() {
		if parsingLiquid {
			fmt.Println("While processing", p.Source())
		}
	}()
	template, err := liquid.Parse(p.Content, nil)
	parsingLiquid = false
	if err != nil {
		err := &os.PathError{Op: "Liquid Error", Path: p.Source(), Err: err}
		return err
	}
	writer := new(bytes.Buffer)
	template.Render(writer, p.TemplateVariables())
	body := writer.Bytes()

	if isMarkdown(p.path) {
		body = blackfriday.MarkdownCommon(body)
	}

	_, err = w.Write(body)
	return err
}

func isMarkdown(path string) bool {
	ext := filepath.Ext(path)
	return site.MarkdownExtensions()[strings.TrimLeft(ext, ".")]
}

// replace each sequence of non-alphanumerics by a single hyphen
func hyphenateNonAlphaSequence(s string) string {
	return nonAlphanumericSequenceMatcher.ReplaceAllString(s, "-")
}

func permalinkTemplateVariables(path string, data VariableMap) map[string]string {
	var (
		collectionName string
		localPath      = path
		ext            = filepath.Ext(path)
		root           = path[:len(path)-len(ext)]
		outputExt      = ext
		name           = filepath.Base(root)
		title          = data.String("title", name)
	)

	if isMarkdown(path) {
		outputExt = ".html"
	}

	if val, found := data["collection"]; found {
		collectionName = val.(string)
		prefix := "_" + collectionName + "/"
		localPath = localPath[len(prefix):]
	}

	return map[string]string{
		"collection": collectionName,
		"ext":        strings.TrimLeft(ext, "."),
		"name":       hyphenateNonAlphaSequence(name),
		"output_ext": strings.TrimLeft(outputExt, "."),
		"path":       localPath,
		"title":      hyphenateNonAlphaSequence(title),
		// TODO year month imonth day i_day short_year hour minute second slug categories
	}
}

func expandPermalinkPattern(pattern string, path string, data VariableMap) (s string, err error) {
	if p, found := permalinkStyles[pattern]; found {
		pattern = p
	}
	templateVariables := permalinkTemplateVariables(path, data)
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = e
			} else {
				panic(r)
			}
		}
	}()
	s = templateVariableMatcher.ReplaceAllStringFunc(pattern, func(m string) string {
		varname := m[1:]
		value, found := templateVariables[varname]
		if !found {
			panic(fmt.Errorf("unknown variable %s in permalink template %s", varname, pattern))
		}
		return value
	})
	return
}
