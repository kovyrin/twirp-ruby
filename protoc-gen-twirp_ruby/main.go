// Copyright 2018 Twitch Interactive, Inc.  All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may not
// use this file except in compliance with the License. A copy of the License is
// located at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// or in the "license" file accompanying this file. This file is distributed on
// an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/protoc-gen-go/descriptor"
	plugin "github.com/golang/protobuf/protoc-gen-go/plugin"

	"github.com/twitchtv/twirp-ruby/internal/gen/typemap"
)

func main() {
	genReq := readGenRequest(os.Stdin)
	genResp := newGenerator(genReq).Generate()
	writeGenResponse(os.Stdout, genResp)
}

func newGenerator(genReq *plugin.CodeGeneratorRequest) *generator {
	return &generator{
		version:             Version,
		genReq:              genReq,
		fileToGoPackageName: make(map[*descriptor.FileDescriptorProto]string),
		reg:                 typemap.New(genReq.ProtoFile),
	}
}

type generator struct {
	version string
	genReq  *plugin.CodeGeneratorRequest

	reg                 *typemap.Registry
	genFiles            []*descriptor.FileDescriptorProto
	fileToGoPackageName map[*descriptor.FileDescriptorProto]string
}

func fileDescSliceContains(slice []*descriptor.FileDescriptorProto, f *descriptor.FileDescriptorProto) bool {
	for _, sf := range slice {
		if f == sf {
			return true
		}
	}
	return false
}

func (g *generator) Generate() *plugin.CodeGeneratorResponse {
	resp := new(plugin.CodeGeneratorResponse)
	g.findProtoFilesToGenerate()

	for _, f := range g.genFiles {
		twirpFileName := noExtension(filePath(f)) + "_twirp.rb"             // e.g. "hello_world/service_twirp.rb"
		pbFileRelativePath := noExtension(onlyBase(filePath(f))) + "_pb.rb" // e.g. "service_pb.rb"

		rubyCode := g.generateRubyCode(f, pbFileRelativePath)
		respFile := &plugin.CodeGeneratorResponse_File{
			Name:    proto.String(twirpFileName),
			Content: proto.String(rubyCode),
		}
		resp.File = append(resp.File, respFile)
	}

	return resp
}

func (g *generator) generateRubyCode(file *descriptor.FileDescriptorProto, pbFileRelativePath string) string {
	b := new(bytes.Buffer)
	print(b, "# Code generated by protoc-gen-twirp_ruby %s, DO NOT EDIT.", g.version)
	print(b, "require 'twirp'")
	print(b, "require_relative '%s'", pbFileRelativePath) // require generated file with messages
	print(b, "")

	indent := indentation(0)
	pkgName := file.GetPackage()

	var modules []string
	if file.Options != nil && file.Options.RubyPackage != nil {
		modules = strings.Split(*file.Options.RubyPackage, "::")
	} else {
		modules = splitRubyConstants(pkgName)
	}

	for _, m := range modules {
		print(b, "%smodule %s", indent, m)
		indent += 1
	}

	for i, service := range file.Service {
		svcName := service.GetName()

		print(b, "%sclass %sService < Twirp::Service", indent, camelCase(svcName))
		if pkgName != "" {
			print(b, "%s  package '%s'", indent, pkgName)
		}
		print(b, "%s  service '%s'", indent, svcName)
		for _, method := range service.GetMethod() {
			rpcName := method.GetName()
			rpcInput := g.toRubyType(method.GetInputType())
			rpcOutput := g.toRubyType(method.GetOutputType())
			print(b, "%s  rpc :%s, %s, %s, :ruby_method => :%s",
				indent, rpcName, rpcInput, rpcOutput, snakeCase(rpcName))
		}
		print(b, "%send", indent)
		print(b, "")

		print(b, "%sclass %sClient < Twirp::Client", indent, camelCase(svcName))
		print(b, "%s  client_for %sService", indent, camelCase(svcName))
		print(b, "%send", indent)
		if i < len(file.Service)-1 {
			print(b, "")
		}
	}

	for range modules {
		indent -= 1
		print(b, "%send", indent)
	}

	return b.String()
}

// protoFilesToGenerate selects descriptor proto files that were explicitly listed on the command-line.
func (g *generator) findProtoFilesToGenerate() {
	for _, name := range g.genReq.FileToGenerate { // explicitly listed on the command-line
		for _, f := range g.genReq.ProtoFile { // all files and everything they import
			if f.GetName() == name { // match
				g.genFiles = append(g.genFiles, f)
				continue
			}
		}
	}

	for _, f := range g.genReq.ProtoFile {
		if fileDescSliceContains(g.genFiles, f) {
			g.fileToGoPackageName[f] = ""
		} else {
			g.fileToGoPackageName[f] = f.GetPackage()
		}
	}
}

// indentation represents the level of Ruby indentation for a block of code. It
// implements the fmt.Stringer interface to output the correct number of spaces
// for the given level of indentation
type indentation int

func (i indentation) String() string {
	return strings.Repeat("  ", int(i))
}

func print(buf *bytes.Buffer, tpl string, args ...interface{}) {
	buf.WriteString(fmt.Sprintf(tpl, args...))
	buf.WriteByte('\n')
}

func filePath(f *descriptor.FileDescriptorProto) string {
	return *f.Name
}

func onlyBase(path string) string {
	return filepath.Base(path)
}

func noExtension(path string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext)
}

func Fail(msgs ...string) {
	s := strings.Join(msgs, " ")
	log.Print("error:", s)
	os.Exit(1)
}

func readGenRequest(r io.Reader) *plugin.CodeGeneratorRequest {
	data, err := ioutil.ReadAll(r)
	if err != nil {
		Fail(err.Error(), "reading input")
	}

	req := new(plugin.CodeGeneratorRequest)
	if err = proto.Unmarshal(data, req); err != nil {
		Fail(err.Error(), "parsing input proto")
	}

	if len(req.FileToGenerate) == 0 {
		Fail("no files to generate")
	}

	return req
}

func writeGenResponse(w io.Writer, resp *plugin.CodeGeneratorResponse) {
	data, err := proto.Marshal(resp)
	if err != nil {
		Fail(err.Error(), "marshaling response")
	}
	_, err = w.Write(data)
	if err != nil {
		Fail(err.Error(), "writing response")
	}
}

// toRubyType converts a protobuf type reference to a Ruby constant.
// e.g. toRubyType("MyMessage", []string{}) => "MyMessage"
// e.g. toRubyType(".foo.my_message", []string{}) => "Foo::MyMessage"
// e.g. toRubyType(".foo.my_message", []string{"Foo"}) => "MyMessage"
// e.g. toRubyType("google.protobuf.Empty", []string{"Foo"}) => "Google::Protobuf::Empty"
func (g *generator) toRubyType(protoType string) string {
	def := g.reg.MessageDefinition(protoType)
	if def == nil {
		panic("could not find message for " + protoType)
	}

	var prefix string
	if pkg := g.fileToGoPackageName[def.File]; pkg != "" {
		prefix = strings.Join(splitRubyConstants(pkg), "::") + "::"
	}

	var name string
	for _, parent := range def.Lineage() {
		name += camelCase(parent.Descriptor.GetName()) + "::"
	}
	name += camelCase(def.Descriptor.GetName())
	return prefix + name
}

// splitRubyConstants converts a namespaced protobuf type (package name or mesasge)
// to a list of names that can be used as Ruby constants.
// e.g. splitRubyConstants("my.cool.package") => ["My", "Cool", "Package"]
// e.g. splitRubyConstants("google.protobuf.Empty") => ["Google", "Protobuf", "Empty"]
func splitRubyConstants(protoPckgName string) []string {
	if protoPckgName == "" {
		return []string{} // no modules
	}

	parts := []string{}
	for _, p := range strings.Split(protoPckgName, ".") {
		parts = append(parts, camelCase(p))
	}
	return parts
}

// snakeCase converts a string from CamelCase to snake_case.
func snakeCase(s string) string {
	var buf bytes.Buffer
	for i, r := range s {
		if unicode.IsUpper(r) && i > 0 {
			fmt.Fprintf(&buf, "_")
		}
		r = unicode.ToLower(r)
		fmt.Fprintf(&buf, "%c", r)
	}
	return buf.String()
}

// camelCase converts a string from snake_case to CamelCased.
// If there is an interior underscore followed by a lower case letter, drop the
// underscore and convert the letter to upper case. There is a remote
// possibility of this rewrite causing a name collision, but it's so remote
// we're prepared to pretend it's nonexistent - since the C++ generator
// lowercases names, it's extremely unlikely to have two fields with different
// capitalizations. In short, _my_field_name_2 becomes XMyFieldName_2.
func camelCase(s string) string {
	if s == "" {
		return ""
	}
	t := make([]byte, 0, 32)
	i := 0
	if s[0] == '_' {
		// Need a capital letter; drop the '_'.
		t = append(t, 'X')
		i++
	}
	// Invariant: if the next letter is lower case, it must be converted
	// to upper case.
	//
	// That is, we process a word at a time, where words are marked by _ or upper
	// case letter. Digits are treated as words.
	for ; i < len(s); i++ {
		c := s[i]
		if c == '_' && i+1 < len(s) && (isASCIILower(s[i+1]) || isASCIIDigit(s[i+1])) {
			continue // Skip the underscore in s.
		}
		// Assume we have a letter now - if not, it's a bogus identifier. The next
		// word is a sequence of characters that must start upper case.
		if isASCIILower(c) {
			c ^= ' ' // Make it a capital letter.
		}
		t = append(t, c) // Guaranteed not lower case.
		// Accept lower case sequence that follows.
		for i+1 < len(s) && (isASCIILower(s[i+1]) || isASCIIDigit(s[i+1])) {
			i++
			t = append(t, s[i])
		}
	}
	return string(t)
}

// Is c an ASCII lower-case letter?
func isASCIILower(c byte) bool {
	return 'a' <= c && c <= 'z'
}

// Is c an ASCII digit?
func isASCIIDigit(c byte) bool {
	return '0' <= c && c <= '9'
}
