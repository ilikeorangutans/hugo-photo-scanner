package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"github.com/nfnt/resize"
	"github.com/rwcarlsen/goexif/exif"
	"github.com/rwcarlsen/goexif/mknote"
	"github.com/rwcarlsen/goexif/tiff"
	"github.com/spf13/hugo/parser"
)

type MakeRelativeFunc func(string) string

func main() {
	hugo := "/home/jakob/src/github.com/ilikeorangutans/photos"
	dataRoot := filepath.Join(hugo, "data/album/")
	staticRoot := filepath.Join(hugo, "static/album/")

	albums, err := findHugoAlbums(hugo)
	if err != nil {
		log.Fatal(err)
	}

	exif.RegisterParsers(mknote.All...)

	var wg sync.WaitGroup
	for _, album := range albums {
		wg.Add(1)
		go func(album AlbumConfig) {
			err := processHugoAlbum(album, dataRoot, staticRoot)
			if err != nil {
				log.Printf("Error processing album: %s", err)
			}
			wg.Done()
		}(album)
	}

	wg.Wait()
}

func processHugoAlbum(albumConfig AlbumConfig, dataRoot string, staticRoot string) error {
	if _, err := os.Stat(albumConfig.SrcDir); os.IsNotExist(err) {
		return fmt.Errorf("source dir %s for album %s not found", albumConfig.SrcDir, albumConfig.Slug)
	}
	dataDir := filepath.Join(dataRoot, albumConfig.Slug)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	staticDir := filepath.Join(staticRoot, albumConfig.Slug)
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		return err
	}

	makeRelative := func(input string) string {
		p, err := filepath.Rel(staticRoot, input)
		if err != nil {
			log.Fatal(err)
		}
		return filepath.Join("album", p)
	}
	album, err := scanAlbum(albumConfig, staticDir, makeRelative)
	if err != nil {
		return err
	}
	writeAlbumToml(dataRoot, album)
	return nil
}

type AlbumConfig struct {
	Slug   string
	SrcDir string
}

func findHugoAlbums(path string) ([]AlbumConfig, error) {
	contentPath := filepath.Join(path, "content", "album")

	result := []AlbumConfig{}
	entries, err := ioutil.ReadDir(contentPath)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		slug := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		log.Printf("Extracted slug %s", slug)

		f, err := os.Open(filepath.Join(contentPath, entry.Name()))
		if err != nil {
			return result, err
		}
		page, err := parser.ReadFrom(f)
		if err != nil {
			return result, err
		}

		m, err := page.Metadata()
		if err != nil {
			return result, err
		}
		x, isOk := m.(map[string]interface{})
		if !isOk {
			log.Fatalf("Got incorrect map value in %s", entry.Name())
		}
		dir, isOk := x["album"].(string)
		if !isOk {
			log.Printf("No album frontmatter setting in %s", entry.Name())
			continue
		}

		albumConfig := AlbumConfig{
			Slug:   slug,
			SrcDir: dir,
		}
		result = append(result, albumConfig)
	}
	return result, nil
}

type Config struct {
	StaticRoot string
	DataRoot   string
}

func (c Config) MakeRelative(p string) string {
	result, _ := filepath.Rel(c.StaticRoot, p)
	return result
}

func scanAlbum(albumConfig AlbumConfig, imageDestDir string, makeRelative MakeRelativeFunc) (Album, error) {
	files, err := ioutil.ReadDir(albumConfig.SrcDir)
	if err != nil {
		return Album{}, err
	}

	imageChannel := make(chan ImageMetaInfo)
	counter := 0
	for _, file := range files {
		if ignoreFile(file) {
			continue
		}

		counter++

		widths := map[string]uint{"small": SMALL_WIDTH}
		if strings.ToLower(file.Name()) == "cover.jpg" {
			widths["medium"] = MEDIUM_WIDTH
			widths["large"] = LARGE_WIDTH
		} else {
			widths["large"] = LARGE_WIDTH
		}

		os.MkdirAll(imageDestDir, 0755)
		go func(path string, widths map[string]uint) {
			info, err := resizeImageTo(path, imageDestDir, makeRelative, widths)
			if err != nil {
				log.Fatal(err)
			}
			imageChannel <- info
		}(filepath.Join(albumConfig.SrcDir, file.Name()), widths)
	}

	images := ImagesByDate{}
	for counter > 0 {
		select {
		case info := <-imageChannel:
			images = append(images, info)
			counter--
		}
	}

	sort.Sort(images)
	album := Album{
		Path:   albumConfig.SrcDir,
		Slug:   albumConfig.Slug,
		Images: images,
	}

	return album, nil
}

const (
	exifTimeLayout = "2006:01:02 15:04:05"
)

func extractDateTime(metadata map[string]interface{}) *time.Time {
	key := "DateTime"
	if _, isOk := metadata[key]; !isOk {
		key = "DateTimeOriginal"
	}

	dateStr := strings.TrimRight(fmt.Sprintf("%s", metadata[key]), "\x00")
	// TODO(bradfitz,mpl): look for timezone offset, GPS time, etc.
	// For now, just always return the time.Local timezone.
	dateTime, err := time.ParseInLocation(exifTimeLayout, dateStr, time.Local)
	if err == nil {
		return &dateTime
	}

	return nil
}

func resizeImageTo(src string, destDir string, makeRelative MakeRelativeFunc, widths map[string]uint) (ImageMetaInfo, error) {
	result := ImageMetaInfo{
		Path: src,
	}
	fileName := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))

	b, err := loadBytes(src)
	if err != nil {
		return result, err
	}

	var metadata map[string]interface{}
	if metadata, err = extractEXIF(bytes.NewBuffer(b)); err != nil {
		log.Printf("Error reading exif from %s: %s", src, err)
	}

	result.Exif = metadata
	result.DateTime = extractDateTime(metadata)

	orientation, has := metadata["Orientation"]
	rotate := 0
	if has {
		switch orientation {
		case 3:
			rotate = 180
		case 6:
			rotate = 90
		case 8:
			rotate = -90
		}
	}

	for suffix, width := range widths {
		dstFile := filepath.Join(destDir, fmt.Sprintf("%s_%s.jpg", fileName, suffix))

		var info ImageInfo
		if _, err := os.Stat(dstFile); err == nil {
			b, err := ioutil.ReadFile(dstFile)
			if err != nil {
				return result, err
			}
			info, err = extractImageInfo(b, makeRelative(dstFile))
			if err != nil {
				return result, err
			}
		} else {
			info, err = resizeImage(bytes.NewBuffer(b), dstFile, width, rotate, makeRelative)
			if err != nil {
				return result, err
			}
		}

		switch suffix {
		case "small":
			result.Small = info
		case "medium":
			result.Medium = info
		case "large":
			result.Large = info
		default:
			log.Fatalf("Unknown size suffix %q", suffix)
		}
	}

	return result, nil
}

func extractImageInfo(b []byte, relativePath string) (ImageInfo, error) {
	c, _, err := image.DecodeConfig(bytes.NewBuffer(b))
	if err != nil {
		return ImageInfo{}, err
	}

	return ImageInfo{
		RelativeURL: relativePath,
		Width:       c.Width,
		Height:      c.Height,
	}, nil
}

func ignoreFile(f os.FileInfo) bool {
	if f.IsDir() {
		return true
	}
	if !strings.HasSuffix(strings.ToLower(f.Name()), ".jpg") {
		return true
	}

	ignoreSuffixes := []string{
		"_small.jpg",
		"_large.jpg",
		"_medium.jpg",
	}

	for _, suffix := range ignoreSuffixes {
		if strings.HasSuffix(f.Name(), suffix) {
			return true
		}
	}

	return false
}

func writeAlbumToml(root string, album Album) {
	log.Printf("Writing gallery.toml for %s", album.Slug)
	tomlDir := filepath.Join(root, album.Slug)
	if err := os.MkdirAll(tomlDir, 0755); err != nil {
		log.Fatalf("Error creating tomlDir %s", tomlDir, err)
	}

	tomlPath := filepath.Join(tomlDir, "album.toml")
	var galleryToml *os.File
	if _, err := os.Stat(tomlPath); os.IsNotExist(err) {
		galleryToml, err = os.Create(tomlPath)
		if err != nil {
			log.Fatal(err)
		}
	} else {
		galleryToml, err = os.OpenFile(tomlPath, os.O_TRUNC|os.O_RDWR, 0644)
		if err != nil {
			log.Fatal(err)
		}
	}
	defer galleryToml.Close()
	encoder := toml.NewEncoder(galleryToml)

	err := encoder.Encode(album)
	if err != nil {
		log.Fatal(err)
	}
}

type Album struct {
	Path   string
	Slug   string
	Images []ImageMetaInfo
}

type ImagesByDate []ImageMetaInfo

func (d ImagesByDate) Len() int      { return len(d) }
func (d ImagesByDate) Swap(i, j int) { d[i], d[j] = d[j], d[i] }
func (d ImagesByDate) Less(i, j int) bool {
	if d[i].DateTime == nil {
		return false
	}
	if d[j].DateTime == nil {
		return true
	}
	return d[i].DateTime.Before(*d[j].DateTime)
}

const (
	SMALL_WIDTH  uint = 600
	LARGE_WIDTH       = 1536
	MEDIUM_WIDTH      = 800
)

var ENCODING_QUALITY map[uint]int = map[uint]int{
	SMALL_WIDTH:  80,
	MEDIUM_WIDTH: 80,
	LARGE_WIDTH:  80,
}

func loadBytes(path string) ([]byte, error) {
	imageFile, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("Could not open raw image: %s", err)
	}
	defer imageFile.Close()
	b, err := ioutil.ReadAll(imageFile)
	if err != nil {
		return nil, fmt.Errorf("Could not read raw image: %s", err)
	}

	return b, nil
}

func resizeImage(b *bytes.Buffer, name string, maxWidth uint, angle int, makeRelative MakeRelativeFunc) (ImageInfo, error) {
	rawJpeg, err := jpeg.Decode(b)
	if err != nil {
		return ImageInfo{}, err
	}
	resized := resize.Resize(maxWidth, 0, rawJpeg, resize.Lanczos3)
	width, height := resized.Bounds().Dx(), resized.Bounds().Dy()
	if angle == 90 || angle == -90 {
		log.Printf("Rotating %s by %d", name, rotate)
		width, height = height, width
		resized = rotate(resized, angle)
	}
	output, err := os.Create(name)
	defer output.Close()
	quality := ENCODING_QUALITY[maxWidth]
	log.Printf("Encoding %s with quality %d", name, quality)
	if err := jpeg.Encode(output, resized, &jpeg.Options{Quality: quality}); err != nil {
		return ImageInfo{}, err
	}
	info := ImageInfo{
		RelativeURL: makeRelative(name),
		Width:       width,
		Height:      height,
	}

	return info, nil
}

func rotate(img image.Image, angle int) image.Image {
	var result *image.NRGBA
	switch angle {
	case -90:
		h, w := img.Bounds().Dx(), img.Bounds().Dy()
		result = image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				result.Set(x, y, img.At(h-1-y, x))
			}
		}
	case 90:
		h, w := img.Bounds().Dx(), img.Bounds().Dy()
		result = image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				result.Set(x, y, img.At(y, w-1-x))
			}
		}
	}
	return result
}

func extractEXIF(r io.Reader) (map[string]interface{}, error) {
	x, err := exif.Decode(r)
	if err != nil {
		return nil, err
	}
	x.DateTime()

	walker := exifWalker{tags: make(map[string]interface{})}
	err = x.Walk(walker)
	if err != nil {
		return nil, err
	}
	return walker.tags, nil
}

type exifWalker struct {
	tags map[string]interface{}
}

func (w exifWalker) Walk(name exif.FieldName, tag *tiff.Tag) error {
	var val interface{}
	var err error
	switch tag.Format() {
	case tiff.StringVal:
		str, err := tag.StringVal()
		if err != nil {
			log.Println(err)
		}
		if strings.Contains(str, "\000") {
			str = ""
		}
		if utf8.ValidString(str) {
			val = str
		}
	case tiff.IntVal:
		val, err = tag.Int(0)
		if err != nil {
			log.Println(err)
		}
	case tiff.FloatVal:
		val, err = tag.Float(0)
		if err != nil {
			log.Println(err)
		}
	case tiff.OtherVal:
		log.Printf("OtherVal: %s", name)
	}
	w.tags[string(name)] = val
	return nil
}

type ImageInfo struct {
	RelativeURL   string
	Width, Height int
}

type ImageMetaInfo struct {
	Path                      string
	DateTime                  *time.Time
	Raw, Small, Medium, Large ImageInfo
	Exif                      map[string]interface{}
}
