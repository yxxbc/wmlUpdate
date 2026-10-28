// Run wml-update and consume its NDJSON event stream.
//
// Usage:
//   dotnet run -- updater.json 1.4.2 [path/to/wml-update]
using System.Diagnostics;
using System.Text.Json;

if (args.Length < 2)
{
    Console.Error.WriteLine("usage: Update updater.json 1.4.2 [path/to/wml-update]");
    return 2;
}

var config = args[0];
var current = args[1];
var binary = args.Length > 2 ? args[2] : Environment.GetEnvironmentVariable("WML_UPDATE") ?? "wml-update";

var psi = new ProcessStartInfo(binary)
{
    RedirectStandardOutput = true,
    RedirectStandardError = false,
    UseShellExecute = false,
};
foreach (var arg in new[] { "--config", config, "--json", "update", "--current-version", current })
{
    psi.ArgumentList.Add(arg);
}

using var process = Process.Start(psi) ?? throw new InvalidOperationException("failed to start updater");
var terminal = false;
while (process.StandardOutput.ReadLine() is { } line)
{
    if (string.IsNullOrWhiteSpace(line)) continue;
    using var doc = JsonDocument.Parse(line);
    var root = doc.RootElement;
    var type = root.GetProperty("type").GetString();
    switch (type)
    {
        case "progress" when root.TryGetProperty("phase", out var phase) && phase.GetString() == "download":
            Console.WriteLine($"downloading: {root.GetProperty("percent").GetDouble():0}%");
            break;
        case "warning":
            Console.Error.WriteLine($"warning: {root.GetProperty("message").GetString()}");
            break;
        case "log":
            Console.WriteLine($"[{root.GetProperty("phase").GetString()}] {root.GetProperty("message").GetString()}");
            break;
        case "error":
            var error = root.GetProperty("error");
            Console.Error.WriteLine($"update failed [{error.GetProperty("code").GetString()}]: {error.GetProperty("message").GetString()}");
            terminal = true;
            break;
        case "result":
            Console.WriteLine(JsonSerializer.Serialize(root.GetProperty("data"), new JsonSerializerOptions { WriteIndented = true }));
            terminal = true;
            break;
    }
}
process.WaitForExit();
if (process.ExitCode != 0) return process.ExitCode;
if (!terminal)
{
    Console.Error.WriteLine("updater exited without a terminal event");
    return 1;
}
return 0;
