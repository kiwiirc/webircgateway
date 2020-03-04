package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var newPlugins []string

func main() {
	findPlugins()
	pluginsGo := "pkg/plugins/plugins.go"
	os.MkdirAll(filepath.Dir(pluginsGo), os.ModePerm)
	file, err := os.Create(pluginsGo)
	if err != nil {
		fmt.Printf("error creating file: %s %s\n", pluginsGo, err)
		return
	}

	file.WriteString("package plugins\n\n")
	file.WriteString("import (\n")
	file.WriteString("\t\"sync\"\n\n")
	file.WriteString("\t\"github.com/kiwiirc/webircgateway/pkg/webircgateway\"\n")

	if len(newPlugins) > 0 {
		file.WriteString("\n")
	}

	for _, v := range newPlugins {
		dir := filepath.Dir(v)
		file.WriteString("\tplugin" + strings.Title(filepath.Base(dir)) + " \"github.com/kiwiirc/webircgateway/" + strings.ReplaceAll(dir, "\\", "/") + "\"\n")
	}

	file.WriteString(")\n\n")
	file.WriteString("func LoadInternalPlugins(gateway *webircgateway.Gateway, pluginsQuit *sync.WaitGroup) {\n")

	if len(newPlugins) == 0 {
		file.WriteString("\t// No internal plugins to load\n")
	}

	for _, v := range newPlugins {
		dir := filepath.Dir(v)
		file.WriteString("\tif arrayContains(gateway.Config.InternalPlugins, \"" + filepath.Base(dir) + "\") {\n")
		file.WriteString("\t\tplugin" + strings.Title(filepath.Base(dir)) + ".Start(gateway, pluginsQuit)\n")
		file.WriteString("\t}\n")
	}
	file.WriteString("}\n\n")

	file.WriteString("func arrayContains(a []string, v string) bool {\n")
	file.WriteString("\tfor _, s := range a {\n")
	file.WriteString("\t\tif s == v {\n")
	file.WriteString("\t\t\treturn true\n")
	file.WriteString("\t\t}\n")
	file.WriteString("\t}\n")
	file.WriteString("\treturn false\n")
	file.WriteString("}\n")
}

func findPlugins() {
	err := filepath.Walk("./plugins", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			fmt.Printf("failure accessing path %q: %v\n", path, err)
			return err
		}
		if strings.HasSuffix(path, "plugin.go") {
			foundPlugin(path)
		}
		return nil
	})
	if err != nil {
		fmt.Printf("error walking the path: %v\n", err)
		return
	}
}

func foundPlugin(path string) {
	file, err := os.Open(path)
	if err != nil {
		fmt.Printf("error reading file: %s %s\n", path, err)
		return
	}
	isMain := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if scanner.Text() == "package main" {
			isMain = true
			break
		}
	}

	newPath := strings.ReplaceAll(path, "\\", "/")

	if !isMain {
		fmt.Printf("adding: %s\n", newPath)
		newPlugins = append(newPlugins, newPath)
	}
}
