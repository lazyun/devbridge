export function reloadWorkbenchDocument(frame) {
  if (!frame) return;
  // Preserve the Core-owned fragment identifying the open task/run/tab.
  try {
    if (frame.contentWindow) {
      frame.contentWindow.location.reload();
      return;
    }
  } catch {
    // A failed/opaque document can still recover from its known local URL.
  }
  frame.src = frame.dataset.source || frame.getAttribute("src");
}
