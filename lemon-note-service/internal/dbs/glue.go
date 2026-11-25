package dbs

import "github.com/LemonITCN/lemon-note/lemon-note-service/internal/pages"

type pagesBlock = pages.BlockDoc

func pagesPatchTitle(t string) pages.Patch { return pages.Patch{Title: &t} }
