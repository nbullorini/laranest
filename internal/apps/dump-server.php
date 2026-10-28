<?php
// Laranest's dump server, run per app with the app's own PHP and vendor/.
// The app's processes get VAR_DUMPER_FORMAT=tcp://127.0.0.1:<port>, so
// Symfony's VarDumper (behind dump() and dd()) sends every dump here instead
// of printing it. Each one is rendered with the project's own dumpers and
// posted to Laranest, which shows it in the panel.
//
// Usage: php dump-server.php <port> <project path> <laranest endpoint>

[, $port, $root, $endpoint] = $argv + [null, null, null, null];
if (!$port || !$root || !$endpoint) {
    fwrite(STDERR, "usage: dump-server.php <port> <project> <endpoint>\n");
    exit(2);
}

require $root.'/vendor/autoload.php';

use Symfony\Component\VarDumper\Cloner\Data;
use Symfony\Component\VarDumper\Cloner\VarCloner;
use Symfony\Component\VarDumper\Dumper\CliDumper;
use Symfony\Component\VarDumper\Dumper\HtmlDumper;
use Symfony\Component\VarDumper\Server\DumpServer;

if (!class_exists(DumpServer::class)) {
    fwrite(STDERR, "This project's symfony/var-dumper is too old to send dumps to a server (it needs 4.1 or later).\n");
    exit(1);
}

// The CSS/JS every HTML dump needs, rendered once: the first output of an
// HtmlDumper is that header followed by the dump.
$html = new HtmlDumper();
$first = $html->dump((new VarCloner())->cloneVar(null), true);
$header = substr($first, 0, (int) strpos($first, '<pre class=sf-dump'));

$text = new CliDumper();
$text->setColors(false);

$server = new DumpServer('127.0.0.1:'.$port);
$server->start();
fwrite(STDOUT, "Receiving dumps on 127.0.0.1:$port\n");

$server->listen(function (Data $data, array $context) use ($html, $text, $header, $endpoint) {
    $source = $context['source'] ?? [];
    $request = $context['request'] ?? null;
    $cli = $context['cli'] ?? null;
    $dump = [
        'time' => date(DATE_ATOM, (int) ($context['timestamp'] ?? time())),
        'html' => $html->dump($data, true),
        'text' => rtrim($text->dump($data, true)),
        'header' => $header,
        'file' => $source['file'] ?? '',
        'line' => $source['line'] ?? 0,
        'name' => $source['name'] ?? '',
        'origin' => $request ? trim(($request['method'] ?? '').' '.($request['uri'] ?? ''))
            : ($cli ? ($cli['command_line'] ?? '') : ''),
    ];
    $body = json_encode($dump, JSON_INVALID_UTF8_SUBSTITUTE | JSON_PARTIAL_OUTPUT_ON_ERROR);
    $ok = @file_get_contents($endpoint, false, stream_context_create(['http' => [
        'method' => 'POST',
        'header' => "Content-Type: application/json\r\n",
        'content' => $body,
        'timeout' => 5,
        'ignore_errors' => true,
    ]]));
    if ($ok === false) {
        fwrite(STDERR, "Could not send a dump to Laranest ($endpoint)\n");
    }
});
