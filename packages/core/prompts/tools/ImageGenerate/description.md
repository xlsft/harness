Creates images with the user's image generation model and saves them as files. You also see the result.

- Use it when the task needs a picture that does not exist yet: an icon, an illustration, a mockup background, a placeholder asset. Do not use it for diagrams or charts, which are better as code (SVG, Mermaid, a plotting script), or for screenshots.
- `prompt` describes the image as a whole: subject, style, composition, colors, any text it must show. The model knows nothing about the conversation, so put everything it needs in the prompt.
- `file_path` is the absolute path of the output, inside the session's folders (the working directories or the scratch folder). Anything else is refused. Use an image extension (.png, .jpg, .webp) or none. If the model returns another format, the extension is changed to match and the result says so. With `count` above 1, the files are named `name-1.png`, `name-2.png` and so on.
- An existing file is replaced only with `overwrite: true`. Choose a new name unless the user wants that file replaced.
- `input_images` are absolute paths of PNG, JPEG or WebP files inside the session's folders. The model edits them or uses them as references. Some models take none; the error says so.
- `size` is `WIDTHxHEIGHT`, such as `1024x1024` or `1536x1024`. Models that use aspect ratios get the closest one. Leave it out for the model's default. `quality` (`low`, `medium` or `high`) applies where the model has the setting.
- Each call costs money and takes a while. Ask for one image unless the user wants options, and read the result before deciding to try again.
