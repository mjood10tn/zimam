<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Which service users a site has and when their password last changed. There
 * is no password column: the panel never keeps one.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('site_credentials', function (Blueprint $table) {
            $table->id();
            $table->foreignId('site_id')->constrained()->cascadeOnDelete();
            // mysql or valkey.
            $table->string('kind');
            $table->string('username');
            $table->timestamp('rotated_at')->nullable();
            $table->timestamps();
            $table->unique(['site_id', 'kind']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('site_credentials');
    }
};
