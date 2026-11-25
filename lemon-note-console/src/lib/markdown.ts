import MarkdownIt from 'markdown-it'

// 智能体回复的 Markdown 渲染：关闭原始 HTML，笔记链接 lemon://page/<id> 变成站内跳转。
const md = new MarkdownIt({ html: false, linkify: true, breaks: true, typographer: false })

const defaultLink = md.renderer.rules.link_open || ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options))
md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
  const t = tokens[idx]
  const href = String(t.attrGet('href') || '')
  const m = href.match(/^lemon:\/\/page\/([a-z0-9]+)/i)
  if (m) {
    t.attrSet('href', '/p/' + m[1])
    t.attrSet('data-page', m[1])
    t.attrJoin('class', 'ln-plink')
  } else if (/^https?:/i.test(href)) {
    t.attrSet('target', '_blank')
    t.attrSet('rel', 'noopener noreferrer nofollow')
  }
  return defaultLink(tokens, idx, options, env, self)
}

md.validateLink = (url: string) => /^(https?:|mailto:|lemon:|\/|#)/i.test(url.trim())

export function renderMarkdown(src: string): string {
  return md.render(src || '')
}
