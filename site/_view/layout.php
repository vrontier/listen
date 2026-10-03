<?php
/** @var array $page */
/** @var string $content */
// Layout of the site pages (landing, contact, 404), styled like /live.
$title       = $page['title'] ?? SITE_NAME;
$fullTitle   = $title === SITE_NAME ? SITE_NAME : $title . ' · ' . SITE_NAME;
$description = $page['description'] ?? '';
$path        = request_path();
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
  <meta property="og:url" content="https://<?= e(SITE_HOST) ?><?= e($path) ?>">
<?php endif; ?>
  <!-- Link previews (text only; the site has no preview image). -->
  <meta property="og:type" content="website">
  <meta property="og:site_name" content="<?= e(SITE_NAME) ?>">
  <meta name="twitter:card" content="summary">
  <meta name="theme-color" content="#07080a">
  <link rel="icon" href="<?= e(asset('favicon.svg')) ?>" type="image/svg+xml">
  <link rel="stylesheet" href="<?= e(asset('css/site.css')) ?>">
  <link rel="stylesheet" href="<?= e(asset('css/live.css')) ?>">
  <link rel="stylesheet" href="<?= e(asset('css/pages.css')) ?>">
</head>
<body class="live page">
  <header class="page__bar">
    <a class="page__brand" href="/">Listening Observatory</a>
    <nav class="page__nav" aria-label="Site">
      <a href="/#streams"<?= $path === '/' ? '' : '' ?>>Streams</a>
      <a href="/#reading">How to read</a>
      <a href="/contact"<?= $path === '/contact' ? ' aria-current="page"' : '' ?>>Contact</a>
      <a href="https://github.com/vrontier/listen" target="_blank" rel="noopener noreferrer">Source</a>
    </nav>
  </header>
  <main class="page__main">
<?= $content ?>
  </main>
  <footer class="page__foot">
    <span><?= e(SITE_HOST) ?></span>
    <span>Code: MIT · Audio remains with its sources</span>
    <a href="https://vrontier.org">A Vrontier project</a>
  </footer>
<?php if (!empty($page['scripts'])): foreach ($page['scripts'] as $js): ?>
  <script src="<?= e(asset($js)) ?>" defer></script>
<?php endforeach; endif; ?>
</body>
</html>
