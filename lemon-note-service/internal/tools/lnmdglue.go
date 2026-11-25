package tools

import "github.com/LemonITCN/lemon-note/lemon-note-service/internal/lnmd"

type lnmdNode = lnmd.Node

func renderNodes(env *Env, nodes []lnmd.Node) string { return lnmd.Render(nodes, env.RenderOpts()) }
