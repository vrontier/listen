<?php
/** @var array $page */
$source = live_source();
?>
  <div class="observatory" data-ws="<?= e(live_ws_url()) ?>" data-tz="<?= e($source['timezone']) ?>">
    <section class="stage" aria-label="Live visualization">
      <div id="terrain" class="stage__canvas" aria-hidden="true"></div>

      <header class="stage__title">
        <h1><?= e($source['name']) ?></h1>
        <p class="stage__sub"><?= e($source['subtitle']) ?></p>
        <p class="stage__meta"><?= implode('<br>', array_map('e', $source['meta'])) ?></p>
      </header>

      <dl class="stage__readout" aria-label="Current signal">
        <div class="readout__time"><dd id="ro-time">—</dd></div>
        <div><dt>Signal state</dt><dd id="ro-state">—</dd></div>
        <div><dt>Harmonicity</dt><dd id="ro-harmonicity">—</dd></div>
        <div><dt>Spectral entropy</dt><dd id="ro-entropy">—</dd></div>
        <div><dt>Novelty</dt><dd id="ro-novelty">—</dd></div>
        <div class="readout__conn"><dd><span id="conn" class="conn" data-state="connecting">connecting</span></dd></div>
      </dl>
    </section>

    <section class="panels" aria-label="Analysis">
      <article class="panel panel--spectrogram">
        <h2>Spectrogram</h2>
        <div class="plot plot--yaxis">
          <ol class="axis axis--y" aria-hidden="true" id="sg-yaxis"></ol>
          <canvas id="spectrogram" width="600" height="160"></canvas>
          <ol class="axis axis--x" aria-hidden="true">
            <li>-60s</li><li>-50s</li><li>-40s</li><li>-30s</li><li>-20s</li><li>-10s</li><li>now</li>
          </ol>
        </div>
      </article>

      <article class="panel panel--spectrum">
        <h2>Frequency spectrum</h2>
        <div class="plot">
          <canvas id="spectrum" width="600" height="160"></canvas>
          <ol class="axis axis--x axis--log" aria-hidden="true" id="sp-xaxis"></ol>
        </div>
      </article>

      <article class="panel panel--features">
        <h2>Detected features</h2>
        <dl class="features">
          <div><dt>Dominant frequency</dt><dd id="f-dominant">—</dd></div>
          <div><dt>Harmonics</dt><dd id="f-harmonics">—</dd></div>
          <div><dt>Spectral centroid</dt><dd id="f-centroid">—</dd></div>
          <div><dt>Spectral flux</dt><dd id="f-flux">—</dd></div>
          <div><dt>Entropy</dt><dd id="f-entropy">—</dd></div>
          <div><dt>RMS energy</dt><dd id="f-energy">—</dd></div>
          <div><dt>Harmonicity</dt><dd id="f-harmonicity">—</dd></div>
          <div><dt>State</dt><dd id="f-state">—</dd></div>
        </dl>
      </article>

      <article class="panel panel--events">
        <h2>Recent events</h2>
        <ol class="events" id="events"><li class="events__empty">Listening…</li></ol>
      </article>

      <article class="panel panel--harmonic">
        <h2>Harmonic map <span class="phase">motif memory · phase 2</span></h2>
        <div class="plot plot--yaxis">
          <ol class="axis axis--y" aria-hidden="true" id="hm-yaxis"></ol>
          <canvas id="harmonic-map" width="800" height="150"></canvas>
          <ol class="axis axis--x" aria-hidden="true">
            <li>-5 min</li><li>-4</li><li>-3</li><li>-2</li><li>-1</li><li>now</li>
          </ol>
        </div>
      </article>

      <article class="panel panel--interpretation">
        <h2>Live interpretation <span class="phase">language layer · phase 3</span></h2>
        <div class="interpretation">
          <p>The narrative layer is not connected yet.</p>
          <p>When it is, short descriptions will be written from the measured
             observations above. It gives language to what was detected; it does
             not decide what happened in the sound.</p>
        </div>
      </article>
    </section>

    <footer class="credit">
      <p>
<?php if (!empty($source['credit'])): $c = $source['credit']; ?>
        Audio: <a href="<?= e($c['url']) ?>" target="_blank" rel="noopener noreferrer"><?= e($c['text']) ?></a><?= !empty($c['note']) ? ', ' . e($c['note']) : '' ?>.
<?php endif; ?>
        Analysis and visualization: <a href="/">Listening Observatory</a> (prototype).
      </p>
    </footer>
  </div>
