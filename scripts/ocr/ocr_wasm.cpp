// The OCR module's interface to its host (packages/client/documents/ocr_tesseract.go). Built by build.sh into a WASI
// reactor. The host mounts the verified trained data read-only at /tessdata, nothing else, and calls:
//
//   ocr_alloc(n) / ocr_free(p)          buffers in the module's memory
//   ocr_init(langs)                     langs is "eng" or "eng+rus", NUL-terminated; 0 on success
//   ocr_recognize(px, w, h, bpl, ppi)   8-bit grey pixels; returns a pointer to the result, NUL-terminated, valid until
//                                       the next call; ocr_result_len gives its length. NULL on failure.
//
// The result has one recognised line per row: confidence (0-100), left, top, right, bottom, text, separated by tabs.
// On failure ocr_error returns a message.

#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <string>

#include <tesseract/baseapi.h>
#include <tesseract/ocrclass.h>
#include <tesseract/resultiterator.h>

// The host's answer to "should recognition stop?": nonzero once the page's time is up. Tesseract asks after every
// word. This replaces wazero's CloseOnContextDone, which leaves the module at every loop back-edge and made recognition
// about ten times slower.
extern "C" __attribute__((import_module("harness"), import_name("ocr_cancelled"))) int host_cancelled(void);

static bool cancelled(void *, int) { return host_cancelled() != 0; }

#define EXPORT(name) extern "C" __attribute__((export_name(#name)))

static tesseract::TessBaseAPI *api = nullptr;
static std::string result;
static std::string error;

EXPORT(ocr_alloc) void *ocr_alloc(size_t n) { return malloc(n); }
EXPORT(ocr_free) void ocr_free(void *p) { free(p); }
EXPORT(ocr_error) const char *ocr_error() { return error.c_str(); }
EXPORT(ocr_result_len) int ocr_result_len() { return static_cast<int>(result.size()); }

EXPORT(ocr_init) int ocr_init(const char *langs) {
  error.clear();
  if (api != nullptr) {
    api->End();
    delete api;
  }
  api = new tesseract::TessBaseAPI();
  if (api->Init("/tessdata", langs, tesseract::OEM_LSTM_ONLY) != 0) {
    error = std::string("could not load the trained data for ") + langs;
    delete api;
    api = nullptr;
    return 1;
  }
  api->SetPageSegMode(tesseract::PSM_AUTO);
  return 0;
}

static void append_clean(std::string &out, const char *s) {
  size_t n = strlen(s);
  while (n > 0 && (s[n - 1] == '\n' || s[n - 1] == ' ' || s[n - 1] == '\r')) {
    n--;
  }
  for (size_t i = 0; i < n; i++) {
    char c = s[i];
    out.push_back(c == '\t' || c == '\n' || c == '\r' ? ' ' : c);
  }
}

EXPORT(ocr_recognize)
const char *ocr_recognize(const uint8_t *px, int w, int h, int bpl, int ppi) {
  error.clear();
  result.clear();
  if (api == nullptr) {
    error = "not initialised";
    return nullptr;
  }
  api->SetImage(px, w, h, 1, bpl);
  if (ppi > 0) {
    api->SetSourceResolution(ppi);
  }
  tesseract::ETEXT_DESC monitor;
  monitor.cancel = cancelled;
  if (api->Recognize(&monitor) != 0) {
    error = host_cancelled() != 0 ? "cancelled" : "recognition failed";
    api->Clear();
    return nullptr;
  }
  tesseract::ResultIterator *it = api->GetIterator();
  if (it != nullptr) {
    const tesseract::PageIteratorLevel level = tesseract::RIL_TEXTLINE;
    if (!it->Empty(level)) {
      do {
        char *text = it->GetUTF8Text(level);
        if (text == nullptr) {
          continue;
        }
        int l, t, r, b;
        it->BoundingBox(level, &l, &t, &r, &b);
        std::string line;
        append_clean(line, text);
        delete[] text;
        if (line.find_first_not_of(' ') == std::string::npos) {
          continue;
        }
        result += std::to_string(static_cast<int>(it->Confidence(level) + 0.5f));
        result += '\t' + std::to_string(l) + '\t' + std::to_string(t) + '\t' + std::to_string(r) + '\t' +
                  std::to_string(b) + '\t' + line + '\n';
      } while (it->Next(level));
    }
    delete it;
  }
  api->Clear();
  return result.c_str();
}
