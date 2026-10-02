<?php
/** @var array $page */
$source = $page['source'];
?>
  <div class="observatory" data-ws="<?= e(live_ws_url($source)) ?>" data-source="<?= e($source['slug']) ?>" data-tz="<?= e($source['timezone']) ?>"<?= $source['audio'] ? ' data-audio="1"' : '' ?>>
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
<?php if ($source['audio']): ?>
        <div class="readout__listen"><dd>
          <div class="strip" id="strip" hidden>
            <button id="listen" class="switch" type="button" role="switch" aria-checked="false">
              <span class="switch__led" aria-hidden="true"></span>
              <span class="switch__body" aria-hidden="true"><span class="switch__lever"></span></span>
              <span class="switch__label">Sound</span>
            </button>
            <div class="knob" id="volume" role="slider" tabindex="0" aria-label="Volume"
                 aria-valuemin="0" aria-valuemax="100" aria-valuenow="63" aria-valuetext="0 dB">
              <span class="knob__label" aria-hidden="true">Volume</span>
              <span class="knob__value" aria-hidden="true">0 dB</span>
            </div>
          </div>
          <span class="strip__status" id="listen-status" aria-live="polite"></span>
        </dd></div>
<?php endif; ?>
      </dl>
    </section>

    <section class="dial" aria-label="Memory dial">
      <div class="teleprinter">
        <span class="teleprinter__label">Interpretation</span>
        <div class="teleprinter__scroll" id="tp-scroll" tabindex="0"
             aria-label="Interpretations, newest first. Scroll for earlier ones.">
          <ol class="teleprinter__list" id="tp-list">
            <li class="teleprinter__wait"><span>Listening… the first interpretation appears within about a minute and a half.</span></li>
          </ol>
        </div>
        <canvas class="jog" id="tp-jog" tabindex="0" role="scrollbar" aria-controls="tp-scroll"
                aria-orientation="vertical" aria-label="Interpretation history wheel" hidden></canvas>
        <button class="teleprinter__more" id="tp-more" type="button" hidden></button>
        <p class="visually-hidden" id="tp-live" aria-live="polite"></p>
      </div>
      <canvas id="dial" aria-hidden="true"></canvas>
      <div id="motif-tip" class="motif-tip" role="tooltip" hidden></div>
      <div class="dial__foot">
        <p class="dial__legend"><span class="dial__title">Memory</span> marks = remembered motifs by frequency ·
          taller = more often · brighter = more recent · band = texture ·
          <span class="dial__needle">needle</span> = dominant now · point at a mark for details</p>
        <button class="dial__export" id="motif-export" type="button"
                title="Download the remembered motifs of this source as a CSV file">⤓ Export CSV</button>
      </div>
    </section>

    <section class="panels" aria-label="Analysis">
      <div class="panels__group panels__group--signal">
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

      <article class="panel panel--harmonic">
        <h2>Harmonic map <span class="phase">coloured by motif</span></h2>
        <div class="plot plot--yaxis">
          <ol class="axis axis--y" aria-hidden="true" id="hm-yaxis"></ol>
          <canvas id="harmonic-map" width="800" height="150"></canvas>
          <ol class="axis axis--x" aria-hidden="true">
            <li>-5 min</li><li>-4</li><li>-3</li><li>-2</li><li>-1</li><li>now</li>
          </ol>
        </div>
      </article>
      </div>

      <div class="panels__group panels__group--observations">
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
          <div><dt>Known motifs</dt><dd id="f-motifs">—</dd></div>
          <div><dt>Dominant (1 h)</dt><dd id="f-dominant-motifs">—</dd></div>
        </dl>
      </article>

      <article class="panel panel--events">
        <h2>Recent events</h2>
        <ol class="events" id="events"><li class="events__empty">Listening…</li></ol>
      </article>


      </div>
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
