import { Extension } from '@tiptap/core'
import { Plugin, PluginKey } from '@tiptap/pm/state'
import { stashUpload } from '@/lib/files'

/** 拖入或粘贴文件：图片插入图片块，音视频插入媒体块，其他插入附件块；节点视图负责上传。 */
export const FileDrop = Extension.create({
  name: 'fileDrop',
  addProseMirrorPlugins() {
    const editor = this.editor
    const insertFiles = (files: File[], pos?: number) => {
      const nodes = files.map((f) => {
        const type = f.type.startsWith('image/') ? 'image' : f.type.startsWith('video/') ? 'video' : f.type.startsWith('audio/') ? 'audio' : 'fileBlock'
        return { type, attrs: { uploadToken: stashUpload(f) } }
      })
      const chain = editor.chain().focus()
      if (pos !== undefined) chain.insertContentAt(pos, nodes as any).run()
      else chain.insertContent(nodes as any).run()
    }
    return [new Plugin({
      key: new PluginKey('lnFileDrop'),
      props: {
        handleDrop(view, event) {
          const files = Array.from(event.dataTransfer?.files || [])
          if (!files.length || !editor.isEditable) return false
          event.preventDefault()
          const coords = view.posAtCoords({ left: event.clientX, top: event.clientY })
          insertFiles(files, coords?.pos)
          return true
        },
        handlePaste(_view, event) {
          const files = Array.from(event.clipboardData?.files || [])
          if (!files.length || !editor.isEditable) return false
          event.preventDefault()
          insertFiles(files)
          return true
        },
      },
    })]
  },
})
