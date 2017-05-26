package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/nfnt/resize"
	"github.com/rwcarlsen/goexif/exif"
	"github.com/rwcarlsen/goexif/mknote"
)

type MakeRelativeFunc func(string) string

func main() {

	hugo := "/home/jakob/src/github.com/ilikeorangutans/photos"
	dataRoot := filepath.Join(hugo, "data/album/")
	staticRoot := filepath.Join(hugo, "static")
	root := "/home/jakob/drobofs/photo/2015 - Philippines"

	log.Printf("Scanning for albums under %s", root)
	albumDirs, err := findAlbumDirs(root)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Found %d album(s), processing now...", len(albumDirs))
	log.Printf("Static files root: %s", staticRoot)
	log.Printf("Data files root: %s", dataRoot)

	config := Config{
		StaticRoot: staticRoot,
		DataRoot:   dataRoot,
	}

	exif.RegisterParsers(mknote.All...)

	var wg sync.WaitGroup
	for _, path := range albumDirs {
		wg.Add(1)
		go func() {
			processAlbum(path, config)
			wg.Done()
		}()
	}

	log.Println("Waiting for processors to be done...")
	wg.Wait()
}

func findAlbumDirs(root string) ([]string, error) {
	albumDirs := []string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			return nil
		}
		if filepath.Base(path) != "album" {
			return nil
		}

		albumDirs = append(albumDirs, path)
		return nil
	})

	return albumDirs, err
}

type Config struct {
	StaticRoot string
	DataRoot   string
}

func (c Config) MakeRelative(p string) string {
	result, _ := filepath.Rel(c.StaticRoot, p)
	return result
}

func processAlbum(path string, config Config) error {
	log.Printf("Processing album %s", path)

	album, err := scanAlbum2(path, config)
	if err != nil {
		log.Fatal(err)
	}
	writeAlbumToml(config.DataRoot, album)

	return nil
}

func scanAlbum2(path string, config Config) (Album, error) {
	slug := strings.Replace(strings.ToLower(filepath.Base(filepath.Dir(path))), " ", "", -1)
	log.Printf("Scanning album %q in %s", slug, path)

	files, err := ioutil.ReadDir(path)
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
		if file.Name() == "cover.jpg" {
			widths["medium"] = MEDIUM_WIDTH
			widths["large"] = LARGE_WIDTH
		} else {
			widths["large"] = LARGE_WIDTH
		}

		imageDestDir := filepath.Join(config.StaticRoot, "album", slug)
		os.MkdirAll(imageDestDir, 0755)
		go func(path string, widths map[string]uint) {
			info, err := resizeImageTo(path, imageDestDir, config, widths)
			if err != nil {
				log.Fatal(err)
			}
			imageChannel <- info
		}(filepath.Join(path, file.Name()), widths)
	}

	images := []ImageMetaInfo{}
	for counter > 0 {
		select {
		case info := <-imageChannel:
			images = append(images, info)
			counter--
		}
	}
	album := Album{
		Path:   path,
		Slug:   slug,
		Images: images,
	}

	return album, nil
}

func resizeImageTo(src string, destDir string, config Config, widths map[string]uint) (ImageMetaInfo, error) {
	log.Printf("Resizing image %s", src)

	result := ImageMetaInfo{
		Path: src,
	}
	fileName := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	for suffix, width := range widths {
		log.Printf("  generating %q with max width  %d", suffix, width)
		dstFile := filepath.Join(destDir, fmt.Sprintf("%s_%s.jpg", fileName, suffix))

		if _, err := os.Stat(dstFile); err == nil {
			log.Printf("  -> not generating %s, already exists", dstFile)
			continue
		}

		b, err := loadBytes(src)
		if err != nil {
			return result, err
		}

		info, err := resizeImage(bytes.NewBuffer(b), dstFile, width, config.MakeRelative)
		if err != nil {
			return result, err
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

func main2() {
	fmt.Println("vim-go")
	//root := "/home/jakob/drobofs/photo"
	hugo := "/home/jakob/src/github.com/ilikeorangutans/photos"
	staticRoot := filepath.Join(hugo, "static")
	albumRoot := filepath.Join(staticRoot, "album")
	dataRoot := filepath.Join(hugo, "data/album/")

	exif.RegisterParsers(mknote.All...)

	entries, err := ioutil.ReadDir(albumRoot)
	if err != nil {
		log.Fatal(err)
	}

	makeRelative := func(s string) string {
		p, _ := filepath.Rel(staticRoot, s)
		return p
	}

	var wg sync.WaitGroup
	for _, entry := range entries {
		if entry.IsDir() {
			path := filepath.Join(albumRoot, entry.Name())
			wg.Add(1)
			go func(path string) {
				defer wg.Done()
				gallery := scanAlbum(path, makeRelative)
				writeAlbumToml(dataRoot, gallery)
			}(path)
		}
	}
	wg.Wait()
}

func writeAlbumToml(root string, album Album) {
	tomlDir := filepath.Join(root, album.Slug)
	log.Printf("Writing album.toml to %s", tomlDir)
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

	encoder.Encode(album)
}

type Album struct {
	Path   string
	Slug   string
	Images []ImageMetaInfo
}

func scanAlbum(path string, makeRelative MakeRelativeFunc) Album {
	files, err := ioutil.ReadDir(path)
	if err != nil {
		log.Fatal(err)
	}

	ignoreSuffixes := []string{
		"_small.jpg",
		"_large.jpg",
		"_medium.jpg",
	}

	imageChannel := make(chan ImageMetaInfo)
	counter := 0

	for _, f := range files {
		if !strings.HasSuffix(strings.ToLower(f.Name()), ".jpg") {
			continue
		}

		ignore := false
		for _, suffix := range ignoreSuffixes {
			if strings.HasSuffix(f.Name(), suffix) {
				ignore = true
				break
			}
		}
		if ignore {
			continue
		}

		if f.Name() == "cover.jpg" {

			b, err := loadBytes(filepath.Join(path, "cover.jpg"))
			if err != nil {
				log.Fatal(err)
			}
			resizeImage(bytes.NewBuffer(b), filepath.Join(path, "cover_small.jpg"), SMALL_WIDTH, makeRelative)
			resizeImage(bytes.NewBuffer(b), filepath.Join(path, "cover_medium.jpg"), MEDIUM_WIDTH, makeRelative)

			continue
		}

		counter++
		go func(path string, makeRelative func(string) string) {
			info := handleImage(path, makeRelative)
			imageChannel <- info
		}(filepath.Join(path, f.Name()), makeRelative)
	}

	images := []ImageMetaInfo{}
	for i := 0; i < counter; i++ {
		info := <-imageChannel
		images = append(images, info)
	}

	sort.Sort(ImagesByDate(images))

	gallery := Album{
		Path:   path,
		Slug:   filepath.Base(path),
		Images: images,
	}

	return gallery
}

type ImagesByDate []ImageMetaInfo

func (d ImagesByDate) Len() int           { return len(d) }
func (d ImagesByDate) Swap(i, j int)      { d[i], d[j] = d[j], d[i] }
func (d ImagesByDate) Less(i, j int) bool { return d[i].DateTime.Before(*d[j].DateTime) }

const (
	SMALL_WIDTH  uint = 255
	LARGE_WIDTH       = 1536
	MEDIUM_WIDTH      = 400
)

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

func handleImage(path string, makeRelative func(string) string) ImageMetaInfo {
	b, err := loadBytes(path)
	if err != nil {
		log.Fatal(err)
	}

	rawImage, _, err := image.Decode(bytes.NewBuffer(b))
	if err != nil {
		log.Fatal(err)
	}
	rawInfo := ImageInfo{
		RelativeURL: makeRelative(path),
		Width:       rawImage.Bounds().Dx(),
		Height:      rawImage.Bounds().Dy(),
	}

	var dt time.Time
	x, err := exif.Decode(bytes.NewBuffer(b))
	if err != nil {
		log.Printf("Could not decode EXIF for %s: %s", path, err)
	} else {
		dt, err = x.DateTime()
		if err != nil {
			log.Println("no datetiem for  ", path)
		} else {
		}
	}

	baseName := filepath.Join(filepath.Dir(path), strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))

	smallInfo, err := resizeImage(bytes.NewBuffer(b), fmt.Sprintf("%s_small.jpg", baseName), SMALL_WIDTH, makeRelative)
	if err != nil {
		log.Println(err)
	}

	largeInfo, err := resizeImage(bytes.NewBuffer(b), fmt.Sprintf("%s_large.jpg", baseName), LARGE_WIDTH, makeRelative)
	if err != nil {
		log.Println(err)
	}

	return ImageMetaInfo{
		Path:     path,
		DateTime: &dt,
		Raw:      rawInfo,
		Small:    smallInfo,
		Large:    largeInfo,
	}
}

func readInfo(name string, makeRelative func(string) string) (ImageInfo, error) {
	f, err := os.Open(name)
	if err != nil {
		return ImageInfo{}, err
	}
	defer f.Close()
	rawImage, _, err := image.Decode(f)
	if err != nil {
		return ImageInfo{}, err
	}
	return ImageInfo{
		RelativeURL: makeRelative(name),
		Width:       rawImage.Bounds().Dx(),
		Height:      rawImage.Bounds().Dy(),
	}, nil
}

func resizeImage(b *bytes.Buffer, name string, maxWidth uint, makeRelative func(string) string) (ImageInfo, error) {
	if _, err := os.Stat(name); err == nil {
		log.Printf("Not generating %s", name)
		return readInfo(name, makeRelative)
	}

	rawJpeg, err := jpeg.Decode(b)
	if err != nil {
		return ImageInfo{}, err
	}
	resized := resize.Resize(maxWidth, 0, rawJpeg, resize.Lanczos3)
	output, err := os.Create(name)
	defer output.Close()
	if err := jpeg.Encode(output, resized, nil); err != nil {
		return ImageInfo{}, err
	}
	info := ImageInfo{
		RelativeURL: makeRelative(name),
		Width:       resized.Bounds().Dx(),
		Height:      resized.Bounds().Dy(),
	}

	return info, nil
}

type ImageInfo struct {
	RelativeURL   string
	Width, Height int
}

type ImageMetaInfo struct {
	Path                      string
	DateTime                  *time.Time
	Raw, Small, Medium, Large ImageInfo
}
