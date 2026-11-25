/// <reference types="vite/client" />
declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<object, object, any>
  export default component
}
declare module 'simple-mind-map'
declare module 'react'
declare module 'react-dom/client'
