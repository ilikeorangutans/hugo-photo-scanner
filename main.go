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
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/nfnt/resize"
)

func main() {
	fmt.Println("vim-go")
	root := "/home/jakob/src/github.com/ilikeorangutans/photos"
	staticRoot := filepath.Join(root, "static")
	galleryRoot := filepath.Join(staticRoot, "gallery")
	dataRoot := filepath.Join(root, "data/gallery/")

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
			gallery := scanGallery(path, makeRelative)
			writeGalleryToml(dataRoot, gallery)
		}
	}
	log.Println("Now waiting for goroutines...")
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
	log.Printf("Reading gallery at %s\n", path)

	files, err := ioutil.ReadDir(path)
	if err != nil {
		log.Fatal(err)
	}

	images := []ImageMetaInfo{}

	ignoreSuffixes := []string{
		"_small.jpg",
		"_large.jpg",
	}

	for _, f := range files {
		if !strings.HasSuffix(strings.ToLower(f.Name()), ".jpg") {
			continue
		}

		ignore := false
		for _, suffix := range ignoreSuffixes {
			if strings.HasSuffix(f.Name(), suffix) {
				ignore = true
				println("Ignoring", f.Name())
				break
			}
		}
		if ignore {
			continue
		}

		images = append(images, handleImage(filepath.Join(path, f.Name()), makeRelative))
	}

	gallery := Gallery{
		Path:   path,
		Slug:   filepath.Base(path),
		Images: images,
	}

	return gallery
}

const (
	SMALL_WIDTH = 255
	LARGE_WIDTH = 2048
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
	log.Printf("Processing %s", path)
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
		Path:  path,
		Raw:   rawInfo,
		Small: smallInfo,
		Large: largeInfo,
	}
}

func resizeImage(b *bytes.Buffer, name string, maxWidth uint, makeRelative func(string) string) (ImageInfo, error) {
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
	Raw, Small, Large ImageInfo
}
