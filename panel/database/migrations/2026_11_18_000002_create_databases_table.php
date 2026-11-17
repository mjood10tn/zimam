<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/** The MySQL databases of each site, as the agent last listed them. */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('databases', function (Blueprint $table) {
            $table->id();
            $table->foreignId('site_id')->constrained()->cascadeOnDelete();
            $table->string('name')->unique();
            // From MySQL's statistics, which lag a little.
            $table->unsignedBigInteger('size_bytes')->default(0);
            // creating, active, dropping or failed.
            $table->string('status');
            $table->text('last_error')->nullable();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('databases');
    }
};
