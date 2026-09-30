<?php
/** @var array $page */
/** @var string $content */
$title       = $page['title'] ?? SITE_NAME;
$fullTitle   = $title === SITE_NAME ? SITE_NAME : $title . ' · ' . SITE_NAME;
$description = $page['description'] ?? '';
?>
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title><?= e($fullTitle) ?></title>
<?php if ($description !== ''): ?>
  <meta name="description" content="<?= e($description) ?>">
  <meta property="og:title" content="<?= e($fullTitle) ?>">
  <meta property="og:description" content="<?= e($description) ?>">
  <meta property="og:url" content="https://<?= e(SITE_HOST) ?>/">
<?php endif; ?>
  <meta name="theme-color" content="#0b0c0e">
  <link rel="icon" href="<?= e(asset('favicon.svg')) ?>" type="image/svg+xml">
  <link rel="stylesheet" href="<?= e(asset('css/site.css')) ?>">
</head>
<body>
  <canvas id="field" aria-hidden="true"></canvas>
  <main class="wrap">
<?= $content ?>
  </main>
  <footer class="wrap footer">
    <span><?= e(SITE_HOST) ?></span>
    <a href="https://vrontier.org">A Vrontier project</a>
  </footer>
  <script src="<?= e(asset('js/field.js')) ?>" defer></script>
</body>
</html>
