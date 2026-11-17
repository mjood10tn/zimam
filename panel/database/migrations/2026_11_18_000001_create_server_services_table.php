<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/** The optional services the panel installs on request, and how their install went. */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('server_services', function (Blueprint $table) {
            $table->id();
            // mysql or valkey.
            $table->string('name')->unique();
            // installing, installed or failed.
            $table->string('status');
            $table->string('version')->nullable();
            $table->text('last_error')->nullable();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('server_services');
    }
};
