// The full-window image viewer of the transcript: which image is open, by URL. The viewer lists every image of the
// current session (components/ImageViewer.vue).
export function useImageViewer() {
  const url = useState<string>('image-viewer', () => '')
  return {
    url,
    open: (u: string) => {
      url.value = u
    },
    close: () => {
      url.value = ''
    }
  }
}
