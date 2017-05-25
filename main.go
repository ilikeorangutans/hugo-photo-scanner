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

func main() {

	hugo := "/home/jakob/src/github.com/ilikeorangutans/photos"
	dataRoot := filepath.Join(hugo, "data/gallery/")
	staticRoot := filepath.Join(hugo, "static")
	root := "/home/jakob/drobofs/photo/2017 - Mexico"

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

	scanGallery(path, config.MakeRelative)

	return nil
}

func main2() {
	fmt.Println("vim-go")
	//root := "/home/jakob/drobofs/photo"
	hugo := "/home/jakob/src/github.com/ilikeorangutans/photos"
	staticRoot := filepath.Join(hugo, "static")
	galleryRoot := filepath.Join(staticRoot, "gallery")
	dataRoot := filepath.Join(hugo, "data/gallery/")

	exif.RegisterParsers(mknote.All...)

	entries, err := ioutil.ReadDir(galleryRoot)
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
			path := filepath.Join(galleryRoot, entry.Name())
			wg.Add(1)
			go func(path string) {
				defer wg.Done()
				gallery := scanGallery(path, makeRelative)
				writeGalleryToml(dataRoot, gallery)
			}(path)
		}
	}
	wg.Wait()
}

func writeGalleryToml(root string, gallery Gallery) {
	tomlDir := filepath.Join(root, gallery.Slug)
	os.MkdirAll(tomlDir, 0755)
	tomlPath := filepath.Join(tomlDir, "gallery.toml")
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

	encoder.Encode(gallery)
}

type Gallery struct {
	Path   string
	Slug   string
	Images []ImageMetaInfo
}

func scanGallery(path string, makeRelative func(string) string) Gallery {
	files, err := ioutil.ReadDir(path)
	if err != nil {
		log.Fatal(err)
	}

	ignoreSuffixes := []string{
		"_small.jpg",
		"_large.jpg",
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

	gallery := Gallery{
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
	Path              string
	DateTime          *time.Time
	Raw, Small, Large ImageInfo
}
