import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  // GitHub Pages serves a project site under /<repo>/, so every asset path
  // the build emits has to be relative to that, not the domain root. Local
  // dev is untouched since dev never uses base for anything but "/".
  base: process.env.VITE_BASE_PATH ?? '/',
})
