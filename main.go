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
	fmt.Println("vim-go")
	root := "/home/jakob/src/github.com/ilikeorangutans/photos"
	staticRoot := filepath.Join(root, "static")
	galleryRoot := filepath.Join(staticRoot, "gallery")
	dataRoot := filepath.Join(root, "data/gallery/")

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
	SMALL_WIDTH = 255
	LARGE_WIDTH = 1536
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

	x, err := exif.Decode(bytes.NewBuffer(b))
	if err != nil {
		log.Printf("Could not decode EXIF for %s: %s", path, err)
	}

	dt, err := x.DateTime()
	if err != nil {
		log.Println("no datetiem for  ", path)
	} else {
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
