<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/** The latest result of each security check (§7.6, §9.7): one row per scope, site and check. */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('security_findings', function (Blueprint $table) {
            $table->id();
            $table->string('scope'); // server or site
            $table->foreignId('site_id')->nullable()->constrained()->cascadeOnDelete();
            $table->string('check');
            $table->string('status'); // pass, warn, fail or inconclusive
            $table->text('detail');
            $table->timestamp('checked_at');
            $table->timestamps();
            // SQLite treats NULLs as distinct, so the outcome upserts by query.
            $table->index(['scope', 'site_id', 'check']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('security_findings');
    }
};
