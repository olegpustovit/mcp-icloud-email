import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({ plugins: [react()], base: '/', build: { rollupOptions: { input: { main: 'index.html', sw: 'src/sw.js' }, output: { entryFileNames: chunk => chunk.name === 'sw' ? 'sw.js' : 'assets/[name]-[hash].js' } } } })
