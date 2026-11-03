<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/** Sites as the panel tracks them (§7.6). */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('sites', function (Blueprint $table) {
            $table->id();
            $table->string('name')->unique();
            // static or php.
            $table->string('type');
            // Null for a static site.
            $table->string('php_version')->nullable();
            // provisioning, active, failed or deleting.
            $table->string('status');
            $table->string('web_directory')->default('public');
            // The agent's template set the site's files were last rendered from.
            $table->unsignedInteger('template_version')->nullable();
            $table->text('last_error')->nullable();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('sites');
    }
};
