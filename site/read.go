package site

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/osteele/gojekyll/collection"
	"github.com/osteele/gojekyll/config"
	"github.com/osteele/gojekyll/pages"
	"github.com/osteele/gojekyll/plugins"
	"github.com/osteele/gojekyll/utils"
)

// FromDirectory reads the configuration file, if it exists.
func FromDirectory(source string, flags config.Flags) (*Site, error) {
	s := New(flags)
	configPath := filepath.Join(source, "_config.yml")
	bytes, err := ioutil.ReadFile(configPath)
	switch {
	case err != nil && os.IsNotExist(err):
		// ok
	case err != nil:
		return nil, err
	default:
		err = config.Unmarshal(bytes, &s.config)
		if err != nil {
			return nil, utils.WrapPathError(err, configPath)
		}
		s.ConfigFile = &configPath
	}
	s.config.Source = source
	s.config.ApplyFlags(s.flags)
	return s, nil
}

// Read loads the site data and files.
func (s *Site) Read() error {
	plugins.Install(s.config.Plugins, s)
	if err := s.readDataFiles(); err != nil {
		return err
	}
	if err := s.readFiles(); err != nil {
		return err
	}
	if err := s.initializeRenderingPipeline(); err != nil {
		return err
	}
	return s.runHooks(func(p plugins.Plugin) error { return p.PostRead(s) })
}

// Reloaded returns a new site read the same source directory, configuration file, and load flags.
func (s *Site) Reloaded(paths []string) (*Site, error) {
	if s.requiresFullReload(paths) {
		fmt.Println("reload everything")
		copy, err := FromDirectory(s.SourceDir(), s.flags)
		if err != nil {
			return nil, err
		}
		s = copy
	}
	return s, s.Read()
}

func (s *Site) requiresFullReload(paths []string) bool {
	for _, path := range paths {
		fmt.Println("test", path)
		switch {
		case path == "_config.yml":
			return true
		case strings.HasPrefix(path, s.config.DataDir):
			return true
		case strings.HasPrefix(path, s.config.LayoutsDir):
			return true
		}
	}
	return false
}

// readFiles scans the source directory and creates pages and collection.
func (s *Site) readFiles() error {
	s.Routes = make(map[string]pages.Document)
	walkFn := func(filename string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relname := utils.MustRel(s.SourceDir(), filename)
		switch {
		case info.IsDir() && s.Exclude(relname):
			return filepath.SkipDir
		case info.IsDir(), s.Exclude(relname):
			return nil
		}
		defaultFrontmatter := s.config.GetFrontMatterDefaults("", relname)
		p, err := pages.NewFile(s, filename, filepath.ToSlash(relname), defaultFrontmatter)
		if err != nil {
			return utils.WrapPathError(err, filename)
		}
		s.AddDocument(p, true)
		return nil
	}
	if err := filepath.Walk(s.SourceDir(), walkFn); err != nil {
		return err
	}
	return s.ReadCollections()
}

// AddDocument adds a document to the site structures.
// It ignores unpublished documents unless config.Unpublished is true.
func (s *Site) AddDocument(d pages.Document, output bool) {
	if d.Published() || s.config.Unpublished {
		s.docs = append(s.docs, d)
		if output {
			s.Routes[d.Permalink()] = d
		}
	}
}

// ReadCollections reads the pages of the collections named in the site configuration.
// It adds each collection's pages to the site map, and creates a template site variable for each collection.
func (s *Site) ReadCollections() error {
	for name, data := range s.config.Collections {
		c := collection.New(s, name, data)
		s.Collections = append(s.Collections, c)
		if err := c.ReadPages(); err != nil {
			return err
		}
		for _, p := range c.Pages() {
			s.AddDocument(p, c.Output())
		}
	}
	return nil
}
