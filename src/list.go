// Copyright 2015 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package drive

import (
	"strconv"
	"strings"

	"github.com/odeke-em/log"
)

const (
	InTrash = 1 << iota
	Folder
	NonFolder
	Minimal
	Shared
	Owners
	CurrentVersion
)

type attribute struct {
	minimal bool
	mask    int
	parent  string
}

type traversalSt struct {
	file     *File
	headPath string
	depth    int
	mask     int
	inTrash  bool
}

func (g *Commands) ListMatches() error {
	matches, err := g.rem.FindMatches(g.opts.Path, g.opts.Sources, g.opts.InTrash)
	if err != nil {
		return err
	}

	spin := g.playabler()
	spin.play()

	traversalCount := 0

	for match := range matches {
		if match == nil {
			continue
		}

		travSt := traversalSt{
			depth:    g.opts.Depth,
			file:     match,
			headPath: g.opts.Path,
			inTrash:  g.opts.InTrash,
			mask:     g.opts.TypeMask,
		}

		traversalCount += 1

		if !g.breadthFirst(travSt, spin) {
			break
		}
	}

	spin.stop()

	if traversalCount < 1 {
		g.log.LogErrln("no matches found!")
	}

	return nil
}

func (g *Commands) List() (err error) {
	resolver := g.rem.FindByPath
	if g.opts.InTrash {
		resolver = g.rem.FindByPathTrashed
	}

	var kvList []*keyValue

	for _, relPath := range g.opts.Sources {
		r, rErr := resolver(relPath)
		if rErr != nil {
			g.log.LogErrf("%v: '%s'\n", rErr, relPath)
			return
		}

		if r == nil {
			g.log.LogErrf("remote: %s is nil\n", strconv.Quote(relPath))
			continue
		}

		parentPath := g.parentPather(relPath)

		if remoteRootLike(parentPath) {
			parentPath = ""
		}
		if remoteRootLike(r.Name) {
			r.Name = ""
		}
		if rootLike(parentPath) {
			parentPath = ""
		}

		kvList = append(kvList, &keyValue{key: parentPath, value: r})
	}

	spin := g.playabler()
	spin.play()
	for _, kv := range kvList {
		if kv == nil || kv.value == nil {
			continue
		}

		travSt := traversalSt{
			depth:    g.opts.Depth,
			file:     kv.value.(*File),
			headPath: kv.key,
			inTrash:  g.opts.InTrash,
			mask:     g.opts.TypeMask,
		}

		if !g.breadthFirst(travSt, spin) {
			break
		}
	}
	spin.stop()

	// No-op for now for explicitly traversing shared content
	if false {
		// TODO: Allow traversal of shared content as well as designated paths
		// Next for shared
		sharedRemotes, sErr := g.rem.FindByPathShared("")
		if sErr == nil {
			opt := attribute{
				minimal: isMinimal(g.opts.TypeMask),
				parent:  "",
				mask:    g.opts.TypeMask,
			}
			for sFile := range sharedRemotes {
				sFile.pretty(g.log, opt)
			}
		}
	}

	return
}

func (f *File) pretty(logy *log.Logger, opt attribute) {
	fmtdPath := sepJoin("/", opt.parent, f.Name)

	if opt.minimal {
		logy.Logf("%s ", fmtdPath)
	} else {
		if f.IsDir {
			logy.Logf("d")
		} else {
			logy.Logf("-")
		}
		if f.Shared {
			logy.Logf("s")
		} else {
			logy.Logf("-")
		}

		if f.UserPermission != nil {
			logy.Logf(" %-10s ", f.UserPermission.Role)
		}
	}

	if owners(opt.mask) && len(f.OwnerNames) >= 1 {
		logy.Logf(" %s ", strings.Join(f.OwnerNames, " & "))
	}

	if version(opt.mask) {
		logy.Logf(" v%d", f.Version)
	}

	if !opt.minimal {
		logy.Logf(" %-10s\t%-10s\t\t%-20s\t%-50s\n", prettyBytes(f.Size), f.Id, f.ModTime, fmtdPath)
	} else {
		logy.Logln()
	}
}

func (g *Commands) breadthFirst(travSt traversalSt, spin *playable) bool {

	opt := attribute{
		minimal: isMinimal(g.opts.TypeMask),
		mask:    travSt.mask,
	}

	opt.parent = ""
	if travSt.headPath != "/" {
		opt.parent = travSt.headPath
	}

	f := travSt.file
	if !f.IsDir {
		f.pretty(g.log, opt)
		return true
	}

	// New head path
	if !(rootLike(opt.parent) && rootLike(f.Name)) {
		opt.parent = sepJoin("/", opt.parent, f.Name)
	}

	// A depth of < 0 means traverse as deep as you can
	if travSt.depth == 0 {
		// At the end of the line, this was successful.
		return true
	} else if travSt.depth > 0 {
		travSt.depth -= 1
	}

	expr := buildExpression(f.Id, travSt.mask, travSt.inTrash)

	req := g.rem.service.Files.List()
	req.Q(expr)
	req.MaxResults(g.opts.PageSize)

	spin.pause()

	fileChan := reqDoPage(req, g.opts.Hidden, g.opts.canPrompt())

	spin.play()

	var children []*File
	onlyFiles := (g.opts.TypeMask & NonFolder) != 0

	for file := range fileChan {
		if file == nil {
			return false
		}
		if isHidden(file.Name, g.opts.Hidden) {
			continue
		}

		if file.IsDir {
			children = append(children, file)
		}

		// The case in which only directories wanted is covered by the buildExpression clause
		// reason being that only folder are allowed to be roots, including the only files clause
		// would result in incorrect traversal since non-folders don't have children.
		// Just don't print it, however, the folder will still be explored.
		if onlyFiles && file.IsDir {
			continue
		}
		file.pretty(g.log, opt)
	}

	if !travSt.inTrash && !g.opts.InTrash {
		for _, file := range children {
			childSt := traversalSt{
				depth:    travSt.depth,
				file:     file,
				headPath: opt.parent,
				inTrash:  travSt.inTrash,
				mask:     g.opts.TypeMask,
			}

			if !g.breadthFirst(childSt, spin) {
				return false
			}
		}
		return true
	}
	return len(children) >= 1
}

func isMinimal(mask int) bool {
	return (mask & Minimal) != 0
}

func owners(mask int) bool {
	return (mask & Owners) != 0
}

func version(mask int) bool {
	return (mask & CurrentVersion) != 0
}
