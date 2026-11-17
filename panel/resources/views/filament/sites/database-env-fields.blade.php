{{-- The database DB_DATABASE is set to, in the Database tab's .env write. --}}
@if ($databases->isNotEmpty())
    <label class="grid gap-1">
        <span class="font-medium">{{ __('zimam.databases.write_env.database') }}</span>
        <select wire:model="envDatabase" class="block w-full rounded-lg border border-gray-300 bg-white p-2 dark:border-white/10 dark:bg-white/5">
            @foreach ($databases as $database)
                <option value="{{ $database->name }}">{{ $database->name }}</option>
            @endforeach
        </select>
    </label>
@endif
@error('envDatabase')
    <p class="text-danger-600 dark:text-danger-400">{{ $message }}</p>
@enderror
